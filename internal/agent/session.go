package agent

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// chunkReply splits an assistant reply into sentence-like chunks to allow
// committing transcript increments only after corresponding audio is emitted.
// Heuristic: split on '.', '?', '!' and newlines, retaining punctuation.
func chunkReply(reply string) []string {
	txt := strings.TrimSpace(reply)
	if txt == "" {
		return nil
	}
	var chunks []string
	var b strings.Builder
	for _, r := range txt {
		switch r {
		case '.', '!', '?':
			b.WriteRune(r)
			chunk := strings.TrimSpace(b.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			b.Reset()
		case '\n', '\r':
			chunk := strings.TrimSpace(b.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	tail := strings.TrimSpace(b.String())
	if tail != "" {
		chunks = append(chunks, tail)
	}
	return chunks
}

// Session orchestrates STT -> LLM -> TTS for a single call.
type Session struct {
	transcriber  Transcriber
	llm          LLM
	tts          TTS
	sink         PCM48kSink
	onTranscript func(text string)
	// onTurn is invoked when a [USER] utterance completes and the assistant has spoken
	// back some or all of its reply. The assistant text provided is exactly what was
	// actually spoken to the user (possibly truncated if interrupted).
	onTurn func(user string, assistantSpoken string)

	mu               sync.Mutex
	speaking         bool
	ttsCancel        context.CancelFunc
	bargeInRequested bool

	// conversation history: alternating [USER]/[ASSISTANT] turns (does not include the in-flight user text)
	history []convTurn
}

type convTurn struct {
	Role string // "USER" or "ASSISTANT"
	Text string
}

// buildConversationPrompt formats all previous turns plus the latest user text with [USER]/[ASSISTANT] labels.
func (s *Session) buildConversationPrompt(latestUser string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, t := range s.history {
		b.WriteString("[")
		b.WriteString(strings.ToUpper(t.Role))
		b.WriteString("] ")
		b.WriteString(t.Text)
		b.WriteString("\n")
	}
	b.WriteString("[USER] ")
	b.WriteString(latestUser)
	return b.String()
}

// appendExchange appends a user/assistant turn pair into the history.
func (s *Session) appendExchange(user, assistant string) {
	s.mu.Lock()
	s.history = append(s.history, convTurn{Role: "USER", Text: user})
	s.history = append(s.history, convTurn{Role: "ASSISTANT", Text: assistant})
	s.mu.Unlock()
}

// NewSession constructs a new Session.
func NewSession(t Transcriber, llm LLM, tts TTS, sink PCM48kSink, onTranscript func(string), onTurn func(string, string)) *Session {
	if sink == nil {
		sink = nopSink{}
	}
	return &Session{transcriber: t, llm: llm, tts: tts, sink: sink, onTranscript: onTranscript, onTurn: onTurn}
}

// Start connects the transcriber and begins processing. It returns a stop function.
func (s *Session) Start(ctx context.Context) (func(), error) {
	if err := s.transcriber.Connect(); err != nil {
		return nil, err
	}

	// Stream live transcripts (optional UI)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case t, ok := <-s.transcriber.GetTranscripts():
				if !ok {
					return
				}
				if s.onTranscript != nil && t != "" {
					s.onTranscript(t)
				}
			}
		}
	}()

	// On finalized utterance -> LLM -> TTS -> sink
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case utterance, ok := <-s.transcriber.Finalize():
				if !ok {
					return
				}
				// Normalize whitespace/punctuation only; do not truncate
				prompt := strings.TrimSpace(utterance)
				if prompt == "" {
					continue
				}
				log.Printf("heard(final): %s", prompt)
				// Build conversation context with [USER]/[ASSISTANT] labels; last message must be [USER]
				// Before we let the assistant speak, ensure sustained silence from user
				// to avoid talking over them. Wait up to a bounded time for a silence window.
				waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
				for waitCtx.Err() == nil {
					// Require at least 500ms without detected voice energy before proceeding
					if !s.transcriber.RecentlyDetectedVoice(500 * time.Millisecond) {
						break
					}
					time.Sleep(50 * time.Millisecond)
				}
				waitCancel()

				convo := s.buildConversationPrompt(prompt)
				ctxLLM, cancel := context.WithTimeout(ctx, 20*time.Second)
				reply, err := s.llm.Generate(ctxLLM, convo)
				cancel()
				if err != nil {
					log.Printf("llm error: %v", err)
					continue
				}
				reply = strings.TrimSpace(reply)
				if reply == "" {
					continue
				}
				// We will log only what is actually spoken later via onTurn
				// Record the exchange in the conversation history for LLM context (full assistant reply)
				s.appendExchange(prompt, reply)
				// Stream TTS in chunks to track what was spoken
				ctxTTS, cancelTTS := context.WithCancel(ctx)
				s.mu.Lock()
				s.speaking = true
				s.ttsCancel = cancelTTS
				s.bargeInRequested = false
				s.mu.Unlock()

				var spokenBuilder strings.Builder
				chunks := chunkReply(reply)
			CHUNK_LOOP:
				for i, chunk := range chunks {
					s.mu.Lock()
					barged := s.bargeInRequested
					s.mu.Unlock()
					if barged {
						break CHUNK_LOOP
					}

					// Stream current chunk
					pcmCh, errCh := s.tts.StreamPCM48k(ctxTTS, chunk)
					openPCM, openErr := true, true
					for openPCM || openErr {
						select {
						case b, ok := <-pcmCh:
							if ok {
								if len(b) > 0 {
									s.mu.Lock()
									drop := s.bargeInRequested
									s.mu.Unlock()
									if !drop {
										s.sink.WritePCM(b)
									}
								}
							} else {
								openPCM = false
							}
						case e, ok := <-errCh:
							if ok && e != nil {
								log.Printf("tts stream error: %v", e)
							}
							openErr = false
						case <-ctx.Done():
							openPCM, openErr = false, false
						}
					}
					// If not barged after finishing this chunk, commit chunk text to spoken builder
					s.mu.Lock()
					barged = s.bargeInRequested
					s.mu.Unlock()
					if !barged {
						spokenBuilder.WriteString(strings.TrimSpace(chunk))
						// Add a single space between chunks when needed
						if i < len(chunks)-1 {
							spokenBuilder.WriteString(" ")
						}
					} else {
						break CHUNK_LOOP
					}
				}

				// capture whether barge-in was requested; then clear speaking state
				s.mu.Lock()
				wasBarged := s.bargeInRequested
				s.speaking = false
				s.ttsCancel = nil
				s.bargeInRequested = false
				s.mu.Unlock()
				cancelTTS()
				if !wasBarged {
					s.sink.FlushTail()
				}

				// If interrupted, append marker at current position
				spokenText := strings.TrimSpace(spokenBuilder.String())
				if wasBarged {
					if len(spokenText) > 0 {
						spokenText = spokenText + " [INTERUPTED BY USER]"
					} else {
						spokenText = "[INTERUPTED BY USER]"
					}
				}
				if s.onTurn != nil {
					// Provide exactly what was spoken to the user for this turn
					s.onTurn(prompt, spokenText)
				}
			}
		}
	}()

	stop := func() {
		_ = s.transcriber.Close()
	}
	return stop, nil
}

// FeedPCM16KLE sends input audio to the transcriber.
func (s *Session) FeedPCM16KLE(pcm []byte) {
	_ = s.transcriber.SendPCM16KLE(pcm)
}

type nopSink struct{}

func (nopSink) WritePCM(_ []byte) {}
func (nopSink) FlushTail()        {}
func (nopSink) Reset()            {}

// IsSpeaking reports whether TTS is currently active for this session.
func (s *Session) IsSpeaking() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speaking
}

// BargeIn cancels current TTS streaming and prevents further audio from being written to the sink.
func (s *Session) BargeIn() {
	s.mu.Lock()
	cancel := s.ttsCancel
	if s.speaking {
		s.bargeInRequested = true
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Drop any queued audio immediately to ensure interruption feels instant
	s.sink.Reset()
}
