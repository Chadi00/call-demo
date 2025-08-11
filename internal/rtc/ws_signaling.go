package rtc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"sync/atomic"

	"github.com/chadiek/call-demo/internal/agent"
	"github.com/chadiek/call-demo/internal/llm"
	"github.com/chadiek/call-demo/internal/transcript"
	"github.com/chadiek/call-demo/internal/tts"
	"github.com/gorilla/websocket"
	"github.com/hraban/opus"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v3"
)

// realtimeWSMessage is a minimal signaling message format compatible with common Realtime APIs.
// Types: "auth", "offer", "answer", "candidate", "ice-complete", "bye", "error".
type realtimeWSMessage struct {
	Type string `json:"type"`
	// auth
	Password string `json:"password,omitempty"`
	// offer/answer
	SDP string `json:"sdp,omitempty"`
	// candidate
	Candidate     string  `json:"candidate,omitempty"`
	SDPMid        *string `json:"sdpMid,omitempty"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex,omitempty"`
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  65536,
	WriteBufferSize: 65536,
	CheckOrigin: func(r *http.Request) bool {
		// Allow any origin for demo use; restrict in production
		return true
	},
}

// ServeWebSocket upgrades to WebSocket and performs offer/answer + trickle ICE signaling.
// It expects messages: auth(optional) -> offer -> candidates... and responds with answer + candidates.
func (h *Handler) ServeWebSocket(w http.ResponseWriter, r *http.Request, iceServersJSON string, authPassword string) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}
	defer func() { _ = conn.Close() }()

	// Simple auth: Authorization: Bearer <pwd> or ?password=... or first message type=auth
	if authPassword != "" {
		if !checkAuthHeaderOrQuery(r, authPassword) {
			// fall back to waiting for an auth message as first frame
			mt, data, rerr := conn.ReadMessage()
			if rerr != nil {
				_ = writeWSJSON(conn, realtimeWSMessage{Type: "error"}, fmt.Errorf("auth required"))
				return
			}
			if mt != websocket.TextMessage {
				_ = writeWSJSON(conn, realtimeWSMessage{Type: "error"}, fmt.Errorf("invalid auth frame"))
				return
			}
			var m realtimeWSMessage
			if jerr := json.Unmarshal(data, &m); jerr != nil || strings.ToLower(m.Type) != "auth" || m.Password != authPassword {
				_ = writeWSJSON(conn, realtimeWSMessage{Type: "error"}, fmt.Errorf("unauthorized"))
				return
			}
		}
	}

	// Expect an offer next (or again if auth was pre-checked)
	// If auth already consumed the first message, we proceed to read until offer
	var offerSDP string
	for {
		mt, data, rerr := conn.ReadMessage()
		if rerr != nil {
			log.Printf("ws read error before offer: %v", rerr)
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		var m realtimeWSMessage
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if strings.ToLower(m.Type) == "offer" && m.SDP != "" {
			offerSDP = m.SDP
			break
		}
		if strings.ToLower(m.Type) == "bye" {
			return
		}
	}

	// Build Pion API with ICE servers
	pcs, api, outTrack, cleanup, err := h.createPeerWithServices(iceServersJSON)
	if err != nil {
		_ = writeWSJSON(conn, realtimeWSMessage{Type: "error"}, err)
		return
	}
	defer cleanup()
	_ = api // reserved for advanced use (stats, setting engine)

	callID := generateCallID()

	// Trickle local candidates to client
	pcs.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			// signal end of candidates
			_ = writeWS(conn, realtimeWSMessage{Type: "ice-complete"})
			return
		}
		init := c.ToJSON()
		msg := realtimeWSMessage{Type: "candidate", Candidate: init.Candidate, SDPMid: init.SDPMid, SDPMLineIndex: init.SDPMLineIndex}
		_ = writeWS(conn, msg)
	})

	// Allow receiving remote trickle candidates from client
	go func() {
		for {
			_, data, rerr := conn.ReadMessage()
			if rerr != nil {
				return
			}
			var m realtimeWSMessage
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch strings.ToLower(m.Type) {
			case "candidate":
				if m.Candidate == "" {
					continue
				}
				_ = pcs.AddICECandidate(webrtc.ICECandidateInit{Candidate: m.Candidate, SDPMid: m.SDPMid, SDPMLineIndex: m.SDPMLineIndex})
			case "bye":
				_ = pcs.Close()
				return
			}
		}
	}()

	// Complete PC setup and send answer
	remoteOffer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}
	if err := pcs.SetRemoteDescription(remoteOffer); err != nil {
		_ = writeWSJSON(conn, realtimeWSMessage{Type: "error"}, err)
		return
	}
	answer, err := pcs.CreateAnswer(nil)
	if err != nil {
		_ = writeWSJSON(conn, realtimeWSMessage{Type: "error"}, err)
		return
	}
	if err := pcs.SetLocalDescription(answer); err != nil {
		_ = writeWSJSON(conn, realtimeWSMessage{Type: "error"}, err)
		return
	}
	local := pcs.LocalDescription()
	if local == nil {
		_ = writeWSJSON(conn, realtimeWSMessage{Type: "error"}, errors.New("no local description"))
		return
	}
	if err := writeWS(conn, realtimeWSMessage{Type: "answer", SDP: local.SDP}); err != nil {
		log.Printf("[%s] ws write answer error: %v", callID, err)
		return
	}

	// Bind media handlers (reusing logic from HandleOffer's OnTrack and session orchestration)
	// We need to replicate critical parts here to avoid blocking answer creation.
	h.attachMediaHandlers(callID, pcs, outTrack)

	// Keep the goroutine alive until PC closes; OnConnectionStateChange handles cleanup
	for {
		time.Sleep(2 * time.Second)
		state := pcs.ConnectionState()
		if state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateDisconnected {
			return
		}
	}
}

func checkAuthHeaderOrQuery(r *http.Request, password string) bool {
	if r == nil || password == "" {
		return false
	}
	if q := r.URL.Query().Get("password"); q != "" && q == password {
		return true
	}
	ah := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(ah), "bearer ") {
		tok := strings.TrimSpace(ah[len("Bearer "):])
		if tok == password {
			return true
		}
	}
	if x := r.Header.Get("X-Auth-Token"); x != "" && x == password {
		return true
	}
	return false
}

func writeWS(conn *websocket.Conn, v interface{}) error {
	return conn.WriteJSON(v)
}

func writeWSJSON(conn *websocket.Conn, base realtimeWSMessage, err error) error {
	if err != nil {
		base.Type = "error"
		// Include a minimal error description
		msg := map[string]string{"type": base.Type, "error": err.Error()}
		return conn.WriteJSON(msg)
	}
	return conn.WriteJSON(base)
}

// createPeerWithServices prepares a PeerConnection with codecs/interceptors, audio sender track,
// and returns a cleanup func. Media and session handlers are attached later to avoid blocking.
func (h *Handler) createPeerWithServices(iceServersJSON string) (*webrtc.PeerConnection, *webrtc.API, *webrtc.TrackLocalStaticSample, func(), error) {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return nil, nil, nil, nil, err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, ir); err != nil {
		return nil, nil, nil, nil, err
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithInterceptorRegistry(ir))

	// Parse ICE servers from JSON or fallback
	servers := parseICEServers(iceServersJSON)
	pcs, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	outTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 1},
		"agent-audio", "agent",
	)
	if err != nil {
		_ = pcs.Close()
		return nil, nil, nil, nil, err
	}
	if _, err := pcs.AddTrack(outTrack); err != nil {
		_ = pcs.Close()
		return nil, nil, nil, nil, err
	}
	cleanup := func() { _ = pcs.Close() }
	return pcs, api, outTrack, cleanup, nil
}

// attachMediaHandlers wires the OnTrack/session orchestration similar to HandleOffer.
func (h *Handler) attachMediaHandlers(callID string, peerConnection *webrtc.PeerConnection, outTrack *webrtc.TrackLocalStaticSample) {
	transcriptionService := transcript.NewAssemblyAIService(h.assemblyAIKey)
	llmClient := llm.NewCerebrasClient(h.cerebrasAPIKey, ifEmpty(h.llmModel, "llama-4-maverick-17b-128e-instruct"))
    ttsClient := tts.NewDeepgramClient(h.deepgramAPIKey, h.deepgramModel)
	var sessPtr atomic.Pointer[agent.Session]
	var pacedPtr atomic.Pointer[OpusPacedWriter]

	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[%s] PeerConnection state: %s", callID, state.String())
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateDisconnected:
			_ = transcriptionService.Close()
			_ = peerConnection.Close()
		}
	})
	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) { log.Printf("[%s] ICE state: %s", callID, state.String()) })
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

		go func() {
			// short confirmation beep
			const (
				opusFrameSamples = 960
			)
			beepFrame := make([]int16, opusFrameSamples)
			// 440hz sine for ~200ms
			samplesTotal := int(48000 * 200 / 1000)
			phase := 0.0
			phaseInc := 2 * 3.14159 * 440.0 / 48000.0
			for generated := 0; generated < samplesTotal; generated += opusFrameSamples {
				for i := 0; i < opusFrameSamples; i++ {
					if generated+i >= samplesTotal {
						beepFrame[i] = 0
						continue
					}
					v := int16(6000.0 * sinf(&phase, phaseInc))
					beepFrame[i] = v
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

		// Connect STT and start session only after we have remote track
		if err := transcriptionService.Connect(); err != nil {
			log.Printf("[%s] Failed to connect to AssemblyAI: %v", callID, err)
			return
		}
		dec, derr := opus.NewDecoder(16000, 1)
		if derr != nil {
			log.Printf("[%s] Opus decoder error: %v", callID, derr)
			return
		}

		sess := agent.NewSession(
			transcriptionService,
			llmClient,
			ttsClient,
			paced,
			nil,
			func(user, assistantSpoken string) {
				if assistantSpoken != "" {
					log.Printf("[%s] SPOKEN assistant: %s", callID, assistantSpoken)
				}
			},
		)
		sessPtr.Store(sess)
		ctxSess, cancelSess := context.WithCancel(context.Background())
		stop, err := sess.Start(ctxSess)
		if err != nil {
			log.Printf("[%s] session start error: %v", callID, err)
		}

		// Cleanup on PC close
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

		go func() {
			pcm16kBuf := make([]byte, 0, 3200*4)
			samples := make([]int16, 1920)
			for {
				pkt, _, readErr := remote.ReadRTP()
				if readErr != nil {
					return
				}
				if len(pkt.Payload) == 0 {
					continue
				}
				n, decErr := dec.Decode(pkt.Payload, samples)
				if decErr != nil {
					continue
				}
				startLen := len(pcm16kBuf)
				need := n * 2
				if cap(pcm16kBuf)-len(pcm16kBuf) < need {
					tmp := make([]byte, len(pcm16kBuf), len(pcm16kBuf)+need+3200)
					copy(tmp, pcm16kBuf)
					pcm16kBuf = tmp
				}
				pcm16kBuf = pcm16kBuf[:len(pcm16kBuf)+need]
				o := pcm16kBuf[startLen:]
				for i := 0; i < n; i++ {
					binary.LittleEndian.PutUint16(o[i*2:(i+1)*2], uint16(samples[i]))
				}
				for len(pcm16kBuf) >= 3200 {
					chunk := pcm16kBuf[:3200]
					_ = transcriptionService.SendPCM16KLE(chunk)
					copy(pcm16kBuf, pcm16kBuf[3200:])
					pcm16kBuf = pcm16kBuf[:len(pcm16kBuf)-3200]
				}
			}
		}()
	})
}

func parseICEServers(iceJSON string) []webrtc.ICEServer {
	var servers []webrtc.ICEServer
	if err := json.Unmarshal([]byte(iceJSON), &servers); err == nil && len(servers) > 0 {
		return servers
	}
	return []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}
}

// Helper sine with phase accumulation for beep
func sinf(phase *float64, inc float64) float64 {
	// Simple Taylor is fine here; using math.Sin directly for clarity
	v := math.Sin(*phase)
	*phase += inc
	return v
}
