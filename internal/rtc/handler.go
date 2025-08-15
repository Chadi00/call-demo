package rtc

import (
	"context"
	"encoding/binary"
	"errors"
	"log"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chadiek/call-demo/internal/agent"
	"github.com/chadiek/call-demo/internal/barge"
	"github.com/chadiek/call-demo/internal/llm"
	"github.com/chadiek/call-demo/internal/transcript"
	"github.com/chadiek/call-demo/internal/tts"
	"github.com/hraban/opus"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v3"
)

type SessionDescription struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

type Handler struct {
	assemblyAIKey  string
	cerebrasAPIKey string
	llmModel       string
	deepgramAPIKey string
	deepgramModel  string
}

func NewHandler(assemblyAIKey string) *Handler { return &Handler{assemblyAIKey: assemblyAIKey} }
func (h *Handler) WithLLM(apiKey, model string) *Handler {
	h.cerebrasAPIKey, h.llmModel = apiKey, model
	return h
}
func (h *Handler) WithTTS(apiKey, model string) *Handler {
	h.deepgramAPIKey, h.deepgramModel = apiKey, model
	return h
}

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
	llmClient := llm.NewCerebrasClient(h.cerebrasAPIKey, ifEmpty(h.llmModel, "llama-4-maverick-17b-128e-instruct"))
	ttsClient := tts.NewDeepgramClient(h.deepgramAPIKey, h.deepgramModel)

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

	var sessPtr atomic.Pointer[agent.Session]
	var pacedPtr atomic.Pointer[OpusPacedWriter]
	peerConnection.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "control" {
			return
		}
		log.Printf("[%s] Control channel opened", callID)
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			cmd := strings.TrimSpace(strings.ToLower(string(msg.Data)))
			switch cmd {
			case "stop", "stop-speaking", "cancel", "barge-in":
				if s := sessPtr.Load(); s != nil {
					(*s).BargeIn()
				}
				if p := pacedPtr.Load(); p != nil {
					(*p).Reset()
				}
			}
		})
	})
	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) { log.Printf("[%s] ICE state: %s", callID, state.String()) })

	peerConnection.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if remote.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		log.Printf("[%s] Remote audio track received: codec=%s", callID, remote.Codec().MimeType)

		paced, err := NewOpusPacedWriter(outTrack)
		if err != nil {
			log.Printf("[%s] Opus encoder error: %v", callID, err)
			return
		}
		pacedPtr.Store(paced)

		const (
			pcm16kChunkBytes = 3200
			opusFrameSamples = 960
		)
		pcm16kBuf := make([]byte, 0, pcm16kChunkBytes*4)

		go func() {
			const beepDuration = 300 * time.Millisecond
			samplesTotal := int(48000 * beepDuration / time.Second)
			phase := 0.0
			phaseInc := 2 * math.Pi * 440.0 / 48000.0
			beepFrame := make([]int16, opusFrameSamples)
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
				tmp := make([]byte, opusFrameSamples*2)
				for i := 0; i < opusFrameSamples; i++ {
					v := uint16(beepFrame[i])
					tmp[2*i] = byte(v)
					tmp[2*i+1] = byte(v >> 8)
				}
				paced.WritePCM(tmp)
			}
		}()

		// Create barge-in engine and wire events
		be := barge.NewEngine(barge.DefaultWebRTCHeadset(), barge.Events{
			OnTTSStop: func(ts time.Time) {
				if s := sessPtr.Load(); s != nil {
					(*s).BargeIn()
				}
				if p := pacedPtr.Load(); p != nil {
					(*p).Reset()
				}
			},
			OnTrigger: func(ts time.Time, cues barge.Cues, pre []byte) {
				// Inject pre-roll into ASR stream
				_ = transcriptionService.SendPCM16KLE(pre)
			},
		})

		sess := agent.NewSession(
			transcriptionService,
			llmClient,
			ttsClient,
			paced,
			nil,
			func(user, assistantSpoken string) {
				transcriptMu.Lock()
				turns = append(turns, convoTurn{Role: "USER", Text: user, At: time.Now()})
				if assistantSpoken != "" {
					turns = append(turns, convoTurn{Role: "ASSISTANT", Text: assistantSpoken, At: time.Now()})
				}
				transcriptMu.Unlock()
				if assistantSpoken != "" {
					log.Printf("[%s] SPOKEN assistant: %s", callID, assistantSpoken)
				} else {
					log.Printf("[%s] SPOKEN assistant: (none)", callID)
				}
			},
		).WithBargeEngine(be)
		sessPtr.Store(sess)

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
						// feed mic to ASR and barge engine
						if err := transcriptionService.SendPCM16KLE(chunk); err != nil {
							log.Printf("[%s] AAI send error: %v", callID, err)
						}
						be.FeedMic16k(chunk)
						copy(pcm16kBuf, pcm16kBuf[pcm16kChunkBytes:])
						pcm16kBuf = pcm16kBuf[:len(pcm16kBuf)-pcm16kChunkBytes]
					}
				}
			}()
		}

		// Removed legacy VAD-only barge ticker; replaced by fusion engine events

		if err := transcriptionService.Connect(); err != nil {
			log.Printf("[%s] Failed to connect to AssemblyAI (assistant replies disabled): %v", callID, err)
		} else {
			dec, derr := opus.NewDecoder(16000, 1)
			if derr != nil {
				log.Printf("[%s] Opus decoder error: %v", callID, derr)
				return
			}
			startMicReader(dec)
			ctxSess, cancelSess := context.WithCancel(context.Background())
			stop, err := sess.Start(ctxSess)
			if err != nil {
				log.Printf("[%s] session start error: %v", callID, err)
			}
			// speaking state is tracked inside session and signaled to barge engine from session
			peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
				if state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateDisconnected {
					cancelSess()
					if stop != nil {
						stop()
					}
					paced.FlushTail()
					time.AfterFunc(400*time.Millisecond, func() { paced.Close() })
				}
			})
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
