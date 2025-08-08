package rtc

import (
	"context"
	"encoding/binary"
	"errors"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/chadiek/call-demo/internal/llm"
	"github.com/chadiek/call-demo/internal/transcript"
	"github.com/chadiek/call-demo/internal/tts"
	"github.com/hraban/opus"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

// SessionDescription is a small DTO to avoid exposing webrtc types in transport.
type SessionDescription struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

// Handler manages WebRTC peer connections and implements live transcription.
type Handler struct {
	assemblyAIKey  string
	cerebrasAPIKey string
	llmModel       string
	elevenAPIKey   string
	elevenVoiceID  string
}

func NewHandler(assemblyAIKey string) *Handler { return &Handler{assemblyAIKey: assemblyAIKey} }
func (h *Handler) WithLLM(apiKey, model string) *Handler {
	h.cerebrasAPIKey, h.llmModel = apiKey, model
	return h
}
func (h *Handler) WithTTS(apiKey, voiceID string) *Handler {
	h.elevenAPIKey, h.elevenVoiceID = apiKey, voiceID
	return h
}

// HandleOffer accepts an SDP offer and returns an SDP answer.
func (h *Handler) HandleOffer(ctx context.Context, offer SessionDescription) (SessionDescription, error) {
	if offer.Type != "offer" || offer.SDP == "" {
		return SessionDescription{}, errors.New("invalid offer")
	}

	callID := generateCallID()

	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return SessionDescription{}, err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, ir); err != nil {
		return SessionDescription{}, err
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithInterceptorRegistry(ir))

	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}})
	if err != nil {
		return SessionDescription{}, err
	}

	outTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 1}, "agent-audio", "agent")
	if err != nil {
		_ = peerConnection.Close()
		return SessionDescription{}, err
	}
	if _, err := peerConnection.AddTrack(outTrack); err != nil {
		_ = peerConnection.Close()
		return SessionDescription{}, err
	}

	transcriptionService := transcript.NewAssemblyAIService(h.assemblyAIKey)
	llmClient := llm.NewCerebrasClient(h.cerebrasAPIKey, ifEmpty(h.llmModel, "gpt-oss-120b"))
	ttsClient := tts.NewElevenLabsClient(h.elevenAPIKey, h.elevenVoiceID)

	type convoTurn struct {
		Role, Text string
		At         time.Time
	}
	var (
		transcriptMu sync.Mutex
		turns        []convoTurn
	)

	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[%s] PeerConnection state: %s", callID, state.String())
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateDisconnected:
			transcriptMu.Lock()
			log.Printf("[%s] Conversation transcript (%d turns):", callID, len(turns))
			for i, t := range turns {
				log.Printf("[%s] %02d %s: %s", callID, i+1, strings.ToUpper(t.Role), t.Text)
			}
			transcriptMu.Unlock()
			_ = transcriptionService.Close()
			_ = peerConnection.Close()
		}
	})

	peerConnection.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "transcripts" {
			return
		}
		log.Printf("[%s] Data channel opened", callID)
		go func() {
			for t := range transcriptionService.GetTranscripts() {
				if t != "" {
					_ = dc.SendText(t)
				}
			}
		}()
	})
	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) { log.Printf("[%s] ICE state: %s", callID, state.String()) })

	peerConnection.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if remote.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		log.Printf("[%s] Remote audio track received: codec=%s", callID, remote.Codec().MimeType)

		// Prepare Opus encoder for outgoing agent audio immediately so we can beep
		enc, err := opus.NewEncoder(48000, 1, opus.AppVoIP)
		if err != nil {
			log.Printf("[%s] Opus encoder error: %v", callID, err)
			return
		}

		const (
			pcm16kChunkBytes = 3200
			opusFrameSamples = 960
			pacerInterval    = 20 * time.Millisecond
		)
		pcm16kBuf := make([]byte, 0, pcm16kChunkBytes*4)
		pcm48kBuf := make([]byte, 0, opusFrameSamples*2*10)
		pcm48kInt16 := make([]int16, opusFrameSamples)
		opusBuf := make([]byte, 4000)
		// ~600ms buffer (30 frames * 20ms)
		opusFrames := make(chan []byte, 30)

		// Pacer
		go func() {
			ticker := time.NewTicker(pacerInterval)
			defer ticker.Stop()
			for range ticker.C {
				select {
				case frame := <-opusFrames:
					if err := outTrack.WriteSample(media.Sample{Data: frame, Duration: pacerInterval}); err != nil {
						log.Printf("[%s] WriteSample error: %v", callID, err)
					}
				default:
				}
			}
		}()

		// Send a short beep once to verify audio path
		go func() {
			const beepDuration = 300 * time.Millisecond
			samplesTotal := int(48000 * beepDuration / time.Second)
			phase := 0.0
			phaseInc := 2 * math.Pi * 440.0 / 48000.0
			beepFrame := make([]int16, opusFrameSamples)
			opusBufBeep := make([]byte, 4000)
			for generated := 0; generated < samplesTotal; generated += opusFrameSamples {
				for i := 0; i < opusFrameSamples; i++ {
					if generated+i >= samplesTotal {
						beepFrame[i] = 0
						continue
					}
					v := math.Sin(phase) * 6000.0
					if v > 32767 {
						v = 32767
					} else if v < -32768 {
						v = -32768
					}
					beepFrame[i] = int16(v)
					phase += phaseInc
				}
				n, e := enc.Encode(beepFrame, opusBufBeep)
				if e == nil && n > 0 {
					pkt := make([]byte, n)
					copy(pkt, opusBufBeep[:n])
					select {
					case opusFrames <- pkt:
					default:
					}
				}
			}
		}()

		// LLM/TTS flow (started only if transcription connects)
		startLLMTTS := func() {
			for utt := range transcriptionService.Finalize() {
				if utt == "" {
					continue
				}
				log.Printf("[%s] USER: %s", callID, utt)
				transcriptMu.Lock()
				turns = append(turns, convoTurn{Role: "user", Text: utt, At: time.Now()})
				transcriptMu.Unlock()

				ctxLLM, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				resp, err := llmClient.Generate(ctxLLM, utt)
				cancel()
				if err != nil {
					log.Printf("[%s] LLM error: %v", callID, err)
					continue
				}
				log.Printf("[%s] ASSISTANT: %s", callID, resp)
				transcriptMu.Lock()
				turns = append(turns, convoTurn{Role: "assistant", Text: resp, At: time.Now()})
				transcriptMu.Unlock()

				if h.elevenAPIKey == "" || h.elevenVoiceID == "" {
					log.Printf("[%s] TTS disabled: ELEVENLABS_API_KEY or ELEVENLABS_VOICE_ID not set", callID)
					continue
				}
				log.Printf("[%s] Starting ElevenLabs TTS voice=%s", callID, h.elevenVoiceID)
				pcmStream, errStream := ttsClient.StreamPCM(context.Background(), resp)
				// Drain until BOTH streams are closed; do not stop early when errStream closes without error
				pcmOpen, errOpen := true, true
				for pcmOpen || errOpen {
					select {
					case b0, ok := <-pcmStream:
						if !ok {
							pcmOpen = false
							continue
						}
						if len(b0) > 0 {
							pcm48kBuf = append(pcm48kBuf, b0...)
							for len(pcm48kBuf) >= opusFrameSamples*2 {
								for i := 0; i < opusFrameSamples; i++ {
									pcm48kInt16[i] = int16(binary.LittleEndian.Uint16(pcm48kBuf[i*2 : (i+1)*2]))
								}
								copy(pcm48kBuf, pcm48kBuf[opusFrameSamples*2:])
								pcm48kBuf = pcm48kBuf[:len(pcm48kBuf)-opusFrameSamples*2]
								n, e := enc.Encode(pcm48kInt16, opusBuf)
								if e == nil {
									frame := make([]byte, n)
									copy(frame, opusBuf[:n])
									opusFrames <- frame
								}
							}
						}
					case e, ok := <-errStream:
						if ok && e != nil {
							log.Printf("[%s] TTS stream error: %v", callID, e)
						}
						// mark error stream handled; do NOT break early when it closes without error
						errOpen = false
					}
				}
				if rem := len(pcm48kBuf); rem > 0 {
					if rem >= opusFrameSamples*2 {
						for len(pcm48kBuf) >= opusFrameSamples*2 {
							for i := 0; i < opusFrameSamples; i++ {
								pcm48kInt16[i] = int16(binary.LittleEndian.Uint16(pcm48kBuf[i*2 : (i+1)*2]))
							}
							copy(pcm48kBuf, pcm48kBuf[opusFrameSamples*2:])
							pcm48kBuf = pcm48kBuf[:len(pcm48kBuf)-opusFrameSamples*2]
							n, e := enc.Encode(pcm48kInt16, opusBuf)
							if e == nil {
								frame := make([]byte, n)
								copy(frame, opusBuf[:n])
								opusFrames <- frame
							}
						}
					}
					if len(pcm48kBuf) > 0 && len(pcm48kBuf) < opusFrameSamples*2 {
						for i := 0; i < opusFrameSamples; i++ {
							pcm48kInt16[i] = 0
						}
						for i := 0; i < len(pcm48kBuf)/2; i++ {
							pcm48kInt16[i] = int16(binary.LittleEndian.Uint16(pcm48kBuf[i*2 : (i+1)*2]))
						}
						n, e := enc.Encode(pcm48kInt16, opusBuf)
						if e == nil {
							frame := make([]byte, n)
							copy(frame, opusBuf[:n])
							opusFrames <- frame
						}
					}
					pcm48kBuf = pcm48kBuf[:0]

					// add a short silence tail to avoid clipping the end (~200ms)
					for i := 0; i < 10; i++ { // 10 frames * 20ms
						for j := 0; j < opusFrameSamples; j++ {
							pcm48kInt16[j] = 0
						}
						n, e := enc.Encode(pcm48kInt16, opusBuf)
						if e == nil && n > 0 {
							frame := make([]byte, n)
							copy(frame, opusBuf[:n])
							opusFrames <- frame
						}
					}

					// drain remaining frames based on backlog size (targets ~600ms)
					if backlog := len(opusFrames); backlog > 0 {
						deadline := time.Now().Add(time.Duration(backlog)*pacerInterval + 200*time.Millisecond)
						for time.Now().Before(deadline) {
							if len(opusFrames) == 0 {
								break
							}
							time.Sleep(10 * time.Millisecond)
						}
					}
				}
			}
		}

		// Mic reader (started only if transcription connects)
		startMicReader := func(dec *opus.Decoder) {
			go func() {
				pcmSamples := make([]int16, 1920)
				for {
					pkt, _, readErr := remote.ReadRTP()
					if readErr != nil {
						log.Printf("[%s] RTP read error: %v", callID, readErr)
						return
					}
					if len(pkt.Payload) == 0 {
						continue
					}
					n, decErr := dec.Decode(pkt.Payload, pcmSamples)
					if decErr != nil {
						log.Printf("[%s] Opus decode error: %v", callID, decErr)
						continue
					}
					startLen := len(pcm16kBuf)
					need := n * 2
					if cap(pcm16kBuf)-len(pcm16kBuf) < need {
						newCap := len(pcm16kBuf) + need + pcm16kChunkBytes
						tmp := make([]byte, len(pcm16kBuf), newCap)
						copy(tmp, pcm16kBuf)
						pcm16kBuf = tmp
					}
					pcm16kBuf = pcm16kBuf[:len(pcm16kBuf)+need]
					o := pcm16kBuf[startLen:]
					for i := 0; i < n; i++ {
						binary.LittleEndian.PutUint16(o[i*2:(i+1)*2], uint16(pcmSamples[i]))
					}
					for len(pcm16kBuf) >= pcm16kChunkBytes {
						chunk := pcm16kBuf[:pcm16kChunkBytes]
						if err := transcriptionService.SendAudio(chunk); err != nil {
							log.Printf("[%s] AAI send error: %v", callID, err)
						}
						copy(pcm16kBuf, pcm16kBuf[pcm16kChunkBytes:])
						pcm16kBuf = pcm16kBuf[:len(pcm16kBuf)-pcm16kChunkBytes]
					}
				}
			}()
		}

		// Try to connect to AssemblyAI; if successful, start mic reader and LLM/TTS flow
		if err := transcriptionService.Connect(); err != nil {
			log.Printf("[%s] Failed to connect to AssemblyAI (assistant replies disabled): %v", callID, err)
		} else {
			// Create decoder for incoming mic audio only after successful connect
			dec, derr := opus.NewDecoder(16000, 1)
			if derr != nil {
				log.Printf("[%s] Opus decoder error: %v", callID, derr)
				return
			}
			startMicReader(dec)
			go startLLMTTS()
		}
	})

	remoteOffer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}
	if err := peerConnection.SetRemoteDescription(remoteOffer); err != nil {
		_ = peerConnection.Close()
		return SessionDescription{}, err
	}
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		_ = peerConnection.Close()
		return SessionDescription{}, err
	}
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	if err := peerConnection.SetLocalDescription(answer); err != nil {
		_ = peerConnection.Close()
		return SessionDescription{}, err
	}
	<-gatherComplete
	local := peerConnection.LocalDescription()
	if local == nil {
		_ = peerConnection.Close()
		return SessionDescription{}, errors.New("no local description")
	}
	return SessionDescription{Type: "answer", SDP: local.SDP}, nil
}

func ifEmpty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
func generateCallID() string { return time.Now().Format("0102150405.000") }
