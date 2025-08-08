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

// SILENCE_THRESHOLD is the base inactivity window required before we consider an utterance complete.
// Keep conservative to avoid cutting user mid-sentence.
const SILENCE_THRESHOLD = 700 * time.Millisecond

// CONTINUATION_EXTENSION is added to the silence threshold when the last word
// suggests the user is likely to continue the sentence (e.g., "and", "or", "if").
// CONTINUATION_EXTENSION extends the threshold when the last word implies continuation.
const CONTINUATION_EXTENSION = 1200 * time.Millisecond

// STABILIZATION_GRACE waits a little after crossing the silence threshold to
// absorb any late transcript updates from the ASR before finalizing.
// STABILIZATION_GRACE absorbs late ASR updates.
const STABILIZATION_GRACE = 250 * time.Millisecond

// AssemblyAI streaming transcription service
type AssemblyAIService struct {
	apiKey      string
	conn        *websocket.Conn
	transcripts chan string
	finalizeCh  chan string
	audioData   chan []byte
	stopCh      chan struct{}
	mu          sync.RWMutex
	connected   bool

	// utterance accumulation
	accMu                   sync.Mutex
	latestFullTranscript    string
	committedFullTranscript string
	lastUpdateTime          time.Time
	// resettable timer to detect end-of-utterance based on inactivity
	silenceTimer *time.Timer
	// last time we detected non-silent voice energy in the incoming PCM
	lastVoiceTime time.Time
}

// AssemblyAI message types
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

// NewAssemblyAIService creates a new transcription service
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

// Finalize returns a channel signaling end-of-utterance with the delta text
func (s *AssemblyAIService) Finalize() <-chan string { return s.finalizeCh }

// Connect establishes WebSocket connection to AssemblyAI
func (s *AssemblyAIService) Connect() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.connected {
		return nil
	}

	if s.apiKey == "" {
		return fmt.Errorf("AssemblyAI API key is empty")
	}

	// Connection parameters
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

	// Start goroutines for handling messages and audio
	go s.handleMessages()
	go s.sendAudioData()
	// silence is now detected via a resettable timer started on each Turn

	log.Println("Successfully connected to AssemblyAI streaming service")
	return nil
}

// SendAudio queues audio data to be sent to AssemblyAI
func (s *AssemblyAIService) SendAudio(audioData []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.connected {
		return fmt.Errorf("not connected to AssemblyAI")
	}
	// Heuristic: compute simple RMS to detect voice activity and update lastVoiceTime
	s.detectVoiceActivity(audioData)
	select {
	case s.audioData <- audioData:
		return nil
	default:
		log.Println("Audio buffer full, dropping packet")
		return nil
	}
}

// SendPCM16KLE implements the agent.Transcriber-friendly method name.
func (s *AssemblyAIService) SendPCM16KLE(pcm []byte) error { return s.SendAudio(pcm) }

// detectVoiceActivity updates lastVoiceTime if PCM buffer contains voice energy above a threshold.
// Expects 16-bit little-endian PCM mono at 16 kHz.
func (s *AssemblyAIService) detectVoiceActivity(pcm []byte) {
	const minSamples = 160 // 10ms at 16kHz
	if len(pcm) < minSamples*2 {
		return
	}
	// Downsample scan: step to reduce CPU
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
	// Threshold tuned conservatively; adjust if needed
	const voiceRMS = 250.0
	if rms >= voiceRMS {
		s.accMu.Lock()
		s.lastVoiceTime = time.Now()
		s.accMu.Unlock()
	}
}

// GetTranscripts returns the channel for receiving transcripts
func (s *AssemblyAIService) GetTranscripts() <-chan string { return s.transcripts }

// Partials is an alias for GetTranscripts for clarity in barge-in usage
func (s *AssemblyAIService) Partials() <-chan string { return s.transcripts }

// RecentlyDetectedVoice reports whether non-silent voice energy was observed within the given window.
func (s *AssemblyAIService) RecentlyDetectedVoice(window time.Duration) bool {
	s.accMu.Lock()
	last := s.lastVoiceTime
	s.accMu.Unlock()
	return time.Since(last) <= window
}

// Close closes the connection and cleanup resources
func (s *AssemblyAIService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connected {
		return nil
	}
	// signal shutdown and stop any active timer
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
	// Best-effort flush of any pending delta before closing channels
	s.flushPendingDelta()
	close(s.audioData)
	close(s.transcripts)
	close(s.finalizeCh)
	log.Println("AssemblyAI connection closed")
	return nil
}

// handleMessages processes incoming WebSocket messages
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

// processMessage handles different message types from AssemblyAI
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
			// stream full transcript fragments for UI
			select {
			case s.transcripts <- msg.Transcript:
			default:
			}
			// record latest full transcript
			s.accMu.Lock()
			s.latestFullTranscript = msg.Transcript
			s.lastUpdateTime = time.Now()
			// reset or start the silence timer; finalize will fire only after inactivity
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
		// Flush any pending delta so last words are not lost
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

// finalizeDueToSilence is invoked after SILENCE_THRESHOLD of inactivity.
// It emits only the delta since the last committed transcript, if significant.
func (s *AssemblyAIService) finalizeDueToSilence() {
	// If we're shutting down, do nothing to avoid sends on closed channels
	select {
	case <-s.stopCh:
		return
	default:
	}

	// First pass check
	s.accMu.Lock()
	now := time.Now()
	// Dynamically extend threshold for continuation-like endings
	threshold := SILENCE_THRESHOLD
	if isContinuationLikely(s.latestFullTranscript) {
		threshold += CONTINUATION_EXTENSION
	}
	sinceText := now.Sub(s.lastUpdateTime)
	sinceVoice := now.Sub(s.lastVoiceTime)
	if sinceText < threshold || sinceVoice < threshold {
		// Not enough inactivity; reschedule the timer for the remaining time window
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

	// Snapshot last update time and release lock to wait for stabilization
	lastUpdateAt := s.lastUpdateTime
	s.accMu.Unlock()

	// Grace period to catch late transcript updates
	time.Sleep(STABILIZATION_GRACE)

	// Second pass validation after grace
	s.accMu.Lock()
	now2 := time.Now()
	// Recompute threshold as transcript may have changed
	threshold2 := SILENCE_THRESHOLD
	if isContinuationLikely(s.latestFullTranscript) {
		threshold2 += CONTINUATION_EXTENSION
	}
	if s.lastUpdateTime.After(lastUpdateAt) {
		// A late update arrived during grace; push the timer forward from now
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
	// Deliver without dropping to guarantee every word is sent downstream.
	select {
	case <-s.stopCh:
		return
	case s.finalizeCh <- delta:
	}
}

// flushPendingDelta sends any remaining uncommitted transcript delta.
// It is best-effort and will not block indefinitely on shutdown.
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

// isContinuationLikely returns true if the last meaningful word indicates the
// speaker is likely to continue (conjunctions, prepositions, fillers).
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
	// Split on non-letters to extract words
	fields := strings.FieldsFunc(trim, func(r rune) bool { return !unicode.IsLetter(r) })
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[len(fields)-1])
}

var continuationWords = map[string]struct{}{
	// Coordinating conjunctions
	"and": {}, "or": {}, "but": {}, "nor": {}, "yet": {}, "so": {},
	// Subordinating conjunctions / conditionals
	"if": {}, "when": {}, "while": {}, "though": {}, "although": {},
	"because": {}, "since": {}, "unless": {}, "until": {}, "whereas": {},
	// Discourse markers / fillers
	"also": {}, "plus": {}, "um": {}, "uh": {}, "like": {},
	// Common prepositions that are awkward sentence endings; extend to await continuation
	"about": {}, "with": {}, "to": {}, "of": {}, "for": {}, "on": {}, "in": {}, "at": {},
}

// sendAudioData sends queued audio data to AssemblyAI
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
