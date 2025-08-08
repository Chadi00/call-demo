package httpserver

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/chadiek/call-demo/internal/config"
	"github.com/chadiek/call-demo/internal/rtc"
)

// Server bundles HTTP router and dependencies.
type Server struct {
	Router http.Handler
}

// New constructs the HTTP server with routes.
func New(cfg config.Config) *Server {
	mux := http.NewServeMux()

	// Health route
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// WebRTC signaling and transcription route
	h := rtc.NewHandler(cfg.AssemblyAIKey).
		WithLLM(cfg.CerebrasKey, cfg.CerebrasModelID).
		WithTTS(cfg.ElevenLabsKey, cfg.ElevenLabsVoiceID)
	mux.HandleFunc("/call", func(w http.ResponseWriter, r *http.Request) {
		// Basic CORS for browser demos
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var offer rtc.SessionDescription
		if err := json.NewDecoder(r.Body).Decode(&offer); err != nil {
			log.Printf("invalid offer: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		answer, err := h.HandleOffer(r.Context(), offer)
		if err != nil {
			log.Printf("webrtc handle offer failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	})

	return &Server{Router: mux}
}
