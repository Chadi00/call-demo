package transcript

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const SILENCE_THRESHOLD = 500 * time.Millisecond

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

	// Start goroutines for handling messages and audio
	go s.handleMessages()
	go s.sendAudioData()
	go s.silenceWatcher()

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
	select {
	case s.audioData <- audioData:
		return nil
	default:
		log.Println("Audio buffer full, dropping packet")
		return nil
	}
}

// GetTranscripts returns the channel for receiving transcripts
func (s *AssemblyAIService) GetTranscripts() <-chan string { return s.transcripts }

// Close closes the connection and cleanup resources
func (s *AssemblyAIService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connected {
		return nil
	}
	close(s.stopCh)
	if s.conn != nil {
		terminateMsg := map[string]string{"type": "Terminate"}
		_ = s.conn.WriteJSON(terminateMsg)
		_ = s.conn.Close()
	}
	s.connected = false
	s.conn = nil
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
			s.accMu.Unlock()
		}
	case "Termination":
		var msg TerminationMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Error unmarshaling Termination message: %v", err)
			return
		}
		log.Printf("AssemblyAI session terminated: AudioDuration=%.2fs, SessionDuration=%.2fs", msg.AudioDurationSeconds, msg.SessionDurationSeconds)
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

// Detect 300ms silence to emit utterance delta
func (s *AssemblyAIService) silenceWatcher() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.accMu.Lock()
			if s.latestFullTranscript != "" && time.Since(s.lastUpdateTime) > SILENCE_THRESHOLD {
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
				if isSignificantUtterance(delta) {
					select {
					case s.finalizeCh <- delta:
					default:
					}
				}
				continue
			}
			s.accMu.Unlock()
		}
	}
}

func isSignificantUtterance(text string) bool {
	// Avoid tiny fragments like "i"; require at least 2 words or 8 chars
	trim := strings.TrimSpace(text)
	if len([]rune(trim)) >= 8 {
		return true
	}
	if trim == "" {
		return false
	}
	words := strings.Fields(trim)
	return len(words) >= 2
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
