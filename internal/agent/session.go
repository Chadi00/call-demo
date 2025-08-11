package agent

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

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

type Session struct {
	transcriber  Transcriber
	llm          LLM
	tts          TTS
	sink         PCM48kSink
	onTranscript func(text string)
	onTurn       func(user string, assistantSpoken string)

	mu               sync.Mutex
	speaking         bool
	ttsCancel        context.CancelFunc
	bargeInRequested bool

	history []convTurn
}

type convTurn struct {
	Role string
	Text string
}

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

func (s *Session) appendExchange(user, assistant string) {
	s.mu.Lock()
	s.history = append(s.history, convTurn{Role: "USER", Text: user})
	s.history = append(s.history, convTurn{Role: "ASSISTANT", Text: assistant})
	s.mu.Unlock()
}

func NewSession(t Transcriber, llm LLM, tts TTS, sink PCM48kSink, onTranscript func(string), onTurn func(string, string)) *Session {
	if sink == nil {
		sink = nopSink{}
	}
	return &Session{transcriber: t, llm: llm, tts: tts, sink: sink, onTranscript: onTranscript, onTurn: onTurn}
}

func (s *Session) Start(ctx context.Context) (func(), error) {
	if err := s.transcriber.Connect(); err != nil {
		return nil, err
	}

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

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case utterance, ok := <-s.transcriber.Finalize():
				if !ok {
					return
				}
				prompt := strings.TrimSpace(utterance)
				if prompt == "" {
					continue
				}
				log.Printf("heard(final): %s", prompt)
				waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
				for waitCtx.Err() == nil {
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
				s.appendExchange(prompt, reply)
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
					s.mu.Lock()
					barged = s.bargeInRequested
					s.mu.Unlock()
					if !barged {
						spokenBuilder.WriteString(strings.TrimSpace(chunk))
						if i < len(chunks)-1 {
							spokenBuilder.WriteString(" ")
						}
					} else {
						break CHUNK_LOOP
					}
				}

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

				spokenText := strings.TrimSpace(spokenBuilder.String())
				if wasBarged {
					if len(spokenText) > 0 {
						spokenText = spokenText + " [INTERUPTED BY USER]"
					} else {
						spokenText = "[INTERUPTED BY USER]"
					}
				}
				if s.onTurn != nil {
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

func (s *Session) FeedPCM16KLE(pcm []byte) {
	_ = s.transcriber.SendPCM16KLE(pcm)
}

type nopSink struct{}

func (nopSink) WritePCM(_ []byte) {}
func (nopSink) FlushTail()        {}
func (nopSink) Reset()            {}

func (s *Session) IsSpeaking() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speaking
}

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
	s.sink.Reset()
}
