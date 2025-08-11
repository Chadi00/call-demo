package transcript

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
)

const SILENCE_THRESHOLD = 700 * time.Millisecond

const CONTINUATION_EXTENSION = 1200 * time.Millisecond

const STABILIZATION_GRACE = 250 * time.Millisecond

type AssemblyAIService struct {
	apiKey      string
	conn        *websocket.Conn
	transcripts chan string
	finalizeCh  chan string
	audioData   chan []byte
	stopCh      chan struct{}
	mu          sync.RWMutex
	connected   bool

	accMu                   sync.Mutex
	latestFullTranscript    string
	committedFullTranscript string
	lastUpdateTime          time.Time
	silenceTimer            *time.Timer
	lastVoiceTime           time.Time
}

type BeginMessage struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	ExpiresAt int64  `json:"expires_at"`
}

type TurnMessage struct {
	Type           string `json:"type"`
	Transcript     string `json:"transcript"`
	TurnFormatted  bool   `json:"turn_is_formatted"`
	AudioStartTime int64  `json:"audio_start_time,omitempty"`
	AudioEndTime   int64  `json:"audio_end_time,omitempty"`
}

type TerminationMessage struct {
	Type                   string  `json:"type"`
	AudioDurationSeconds   float64 `json:"audio_duration_seconds"`
	SessionDurationSeconds float64 `json:"session_duration_seconds"`
}

type ErrorMessage struct {
	Type  string `json:"type"`
	Error string `json:"error"`
}

func NewAssemblyAIService(apiKey string) *AssemblyAIService {
	s := &AssemblyAIService{
		apiKey:      apiKey,
		transcripts: make(chan string, 100),
		finalizeCh:  make(chan string, 10),
		audioData:   make(chan []byte, 1000),
		stopCh:      make(chan struct{}),
	}
	return s
}

func (s *AssemblyAIService) Finalize() <-chan string { return s.finalizeCh }

func (s *AssemblyAIService) Connect() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.connected {
		return nil
	}

	if s.apiKey == "" {
		return fmt.Errorf("AssemblyAI API key is empty")
	}

	params := url.Values{}
	params.Set("sample_rate", "16000")
	params.Set("format_turns", "false")
	params.Set("encoding", "pcm_s16le")

	wsURL := fmt.Sprintf("wss://streaming.assemblyai.com/v3/ws?%s", params.Encode())

	headers := map[string][]string{
		"Authorization": {s.apiKey},
	}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}

	log.Printf("Connecting to AssemblyAI at: %s", wsURL)
	keyPreview := s.apiKey
	if len(keyPreview) > 8 {
		keyPreview = keyPreview[:8]
	}
	log.Printf("Using API key: %s...", keyPreview)

	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		if resp != nil {
			log.Printf("AssemblyAI connection failed with status: %d", resp.StatusCode)
		}
		return fmt.Errorf("failed to connect to AssemblyAI: %w", err)
	}

	s.conn = conn
	s.connected = true
	s.lastUpdateTime = time.Now()
	s.lastVoiceTime = time.Now()

	go s.handleMessages()
	go s.sendAudioData()

	log.Println("Successfully connected to AssemblyAI streaming service")
	return nil
}

func (s *AssemblyAIService) SendAudio(audioData []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.connected {
		return fmt.Errorf("not connected to AssemblyAI")
	}
	s.detectVoiceActivity(audioData)
	select {
	case s.audioData <- audioData:
		return nil
	default:
		log.Println("Audio buffer full, dropping packet")
		return nil
	}
}

func (s *AssemblyAIService) SendPCM16KLE(pcm []byte) error { return s.SendAudio(pcm) }

func (s *AssemblyAIService) detectVoiceActivity(pcm []byte) {
	const minSamples = 160 // 10ms at 16kHz
	if len(pcm) < minSamples*2 {
		return
	}
	step := 2            // every sample
	if len(pcm) > 3200 { // if it's a bigger chunk, sample sparsely
		step = 4
	}
	var sumSquares float64
	count := 0
	for i := 0; i+1 < len(pcm); i += 2 * step {
		v := int16(binary.LittleEndian.Uint16(pcm[i : i+2]))
		sumSquares += float64(v) * float64(v)
		count++
	}
	if count == 0 {
		return
	}
	rms := math.Sqrt(sumSquares / float64(count))
	const voiceRMS = 250.0
	if rms >= voiceRMS {
		s.accMu.Lock()
		s.lastVoiceTime = time.Now()
		s.accMu.Unlock()
	}
}

func (s *AssemblyAIService) GetTranscripts() <-chan string { return s.transcripts }

func (s *AssemblyAIService) Partials() <-chan string { return s.transcripts }

func (s *AssemblyAIService) RecentlyDetectedVoice(window time.Duration) bool {
	s.accMu.Lock()
	last := s.lastVoiceTime
	s.accMu.Unlock()
	return time.Since(last) <= window
}

func (s *AssemblyAIService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connected {
		return nil
	}
	close(s.stopCh)
	if s.silenceTimer != nil {
		_ = s.silenceTimer.Stop()
		s.silenceTimer = nil
	}
	if s.conn != nil {
		terminateMsg := map[string]string{"type": "Terminate"}
		_ = s.conn.WriteJSON(terminateMsg)
		_ = s.conn.Close()
	}
	s.connected = false
	s.conn = nil
	s.flushPendingDelta()
	close(s.audioData)
	close(s.transcripts)
	close(s.finalizeCh)
	log.Println("AssemblyAI connection closed")
	return nil
}

func (s *AssemblyAIService) handleMessages() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in handleMessages: %v", r)
		}
	}()
	for {
		select {
		case <-s.stopCh:
			return
		default:
			s.mu.RLock()
			conn := s.conn
			s.mu.RUnlock()
			if conn == nil {
				return
			}
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Printf("Error reading message: %v", err)
				return
			}
			s.processMessage(message)
		}
	}
}

func (s *AssemblyAIService) processMessage(message []byte) {
	var baseMsg map[string]interface{}
	if err := json.Unmarshal(message, &baseMsg); err != nil {
		log.Printf("Error unmarshaling message: %v", err)
		return
	}
	msgType, ok := baseMsg["type"].(string)
	if !ok {
		log.Printf("Message missing type field")
		return
	}
	switch msgType {
	case "Begin":
		var msg BeginMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Error unmarshaling Begin message: %v", err)
			return
		}
		expires := time.Unix(msg.ExpiresAt, 0).Format(time.RFC3339)
		log.Printf("AssemblyAI session began: ID=%s, ExpiresAt=%s", msg.ID, expires)
	case "Turn":
		var msg TurnMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Error unmarshaling Turn message: %v", err)
			return
		}
		if msg.Transcript != "" {
			select {
			case s.transcripts <- msg.Transcript:
			default:
			}
			s.accMu.Lock()
			s.latestFullTranscript = msg.Transcript
			s.lastUpdateTime = time.Now()
			if s.silenceTimer == nil {
				s.silenceTimer = time.AfterFunc(SILENCE_THRESHOLD, s.finalizeDueToSilence)
			} else {
				_ = s.silenceTimer.Stop()
				s.silenceTimer.Reset(SILENCE_THRESHOLD)
			}
			s.accMu.Unlock()
		}
	case "Termination":
		var msg TerminationMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Error unmarshaling Termination message: %v", err)
			return
		}
		log.Printf("AssemblyAI session terminated: AudioDuration=%.2fs, SessionDuration=%.2fs", msg.AudioDurationSeconds, msg.SessionDurationSeconds)
		s.flushPendingDelta()
	case "Error":
		var msg ErrorMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Error unmarshaling Error message: %v", err)
			return
		}
		log.Printf("AssemblyAI error: %s", msg.Error)
	default:
		log.Printf("Unknown message type: %s", msgType)
	}
}

func (s *AssemblyAIService) finalizeDueToSilence() {
	select {
	case <-s.stopCh:
		return
	default:
	}

	s.accMu.Lock()
	now := time.Now()
	threshold := SILENCE_THRESHOLD
	if isContinuationLikely(s.latestFullTranscript) {
		threshold += CONTINUATION_EXTENSION
	}
	sinceText := now.Sub(s.lastUpdateTime)
	sinceVoice := now.Sub(s.lastVoiceTime)
	if sinceText < threshold || sinceVoice < threshold {
		wait := threshold
		if rem := threshold - sinceText; sinceText < threshold && rem < wait {
			wait = rem
		}
		if rem := threshold - sinceVoice; sinceVoice < threshold && rem < wait {
			wait = rem
		}
		if wait < 10*time.Millisecond {
			wait = 10 * time.Millisecond
		}
		if s.silenceTimer == nil {
			s.silenceTimer = time.AfterFunc(wait, s.finalizeDueToSilence)
		} else {
			_ = s.silenceTimer.Stop()
			s.silenceTimer.Reset(wait)
		}
		s.accMu.Unlock()
		return
	}

	lastUpdateAt := s.lastUpdateTime
	s.accMu.Unlock()

	time.Sleep(STABILIZATION_GRACE)

	s.accMu.Lock()
	now2 := time.Now()
	threshold2 := SILENCE_THRESHOLD
	if isContinuationLikely(s.latestFullTranscript) {
		threshold2 += CONTINUATION_EXTENSION
	}
	if s.lastUpdateTime.After(lastUpdateAt) {
		wait := threshold2
		if rem := threshold2 - now2.Sub(s.lastUpdateTime); rem > 10*time.Millisecond && rem < wait {
			wait = rem
		}
		if s.silenceTimer == nil {
			s.silenceTimer = time.AfterFunc(wait, s.finalizeDueToSilence)
		} else {
			_ = s.silenceTimer.Stop()
			s.silenceTimer.Reset(wait)
		}
		s.accMu.Unlock()
		return
	}

	latest := s.latestFullTranscript
	base := s.committedFullTranscript
	delta := strings.TrimSpace(strings.TrimPrefix(latest, base))
	if delta == "" && base != "" {
		if idx := strings.LastIndex(latest, base); idx >= 0 && idx+len(base) <= len(latest) {
			delta = strings.TrimSpace(latest[idx+len(base):])
		}
	}
	s.committedFullTranscript = latest
	s.accMu.Unlock()

	if strings.TrimSpace(delta) == "" {
		return
	}
	select {
	case <-s.stopCh:
		return
	case s.finalizeCh <- delta:
	}
}

func (s *AssemblyAIService) flushPendingDelta() {
	s.accMu.Lock()
	latest := s.latestFullTranscript
	base := s.committedFullTranscript
	delta := strings.TrimSpace(strings.TrimPrefix(latest, base))
	if delta == "" && base != "" {
		if idx := strings.LastIndex(latest, base); idx >= 0 && idx+len(base) <= len(latest) {
			delta = strings.TrimSpace(latest[idx+len(base):])
		}
	}
	s.committedFullTranscript = latest
	s.accMu.Unlock()
	if strings.TrimSpace(delta) == "" {
		return
	}
	select {
	case s.finalizeCh <- delta:
	case <-time.After(200 * time.Millisecond):
		log.Printf("AssemblyAI flush: timed out delivering final delta")
	}
}

func isContinuationLikely(text string) bool {
	w := lastWord(text)
	if w == "" {
		return false
	}
	_, ok := continuationWords[w]
	return ok
}

func lastWord(text string) string {
	trim := strings.TrimSpace(text)
	if trim == "" {
		return ""
	}
	fields := strings.FieldsFunc(trim, func(r rune) bool { return !unicode.IsLetter(r) })
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[len(fields)-1])
}

var continuationWords = map[string]struct{}{
	"and": {}, "or": {}, "but": {}, "nor": {}, "yet": {}, "so": {},
	"if": {}, "when": {}, "while": {}, "though": {}, "although": {},
	"because": {}, "since": {}, "unless": {}, "until": {}, "whereas": {},
	"also": {}, "plus": {}, "um": {}, "uh": {}, "like": {},
	"about": {}, "with": {}, "to": {}, "of": {}, "for": {}, "on": {}, "in": {}, "at": {},
}

func (s *AssemblyAIService) sendAudioData() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in sendAudioData: %v", r)
		}
	}()
	for {
		select {
		case <-s.stopCh:
			return
		case audioData, ok := <-s.audioData:
			if !ok {
				return
			}
			s.mu.RLock()
			conn := s.conn
			s.mu.RUnlock()
			if conn != nil {
				if err := conn.WriteMessage(websocket.BinaryMessage, audioData); err != nil {
					log.Printf("Error sending audio data: %v", err)
					return
				}
			}
		}
	}
}
