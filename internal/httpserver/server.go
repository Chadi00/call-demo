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

	// WebRTC signaling and transcription routes
	h := rtc.NewHandler(cfg.AssemblyAIKey).
		WithLLM(cfg.CerebrasKey, cfg.CerebrasModelID).
		WithTTS(cfg.DeepgramKey, cfg.DeepgramTTSModel)
	mux.HandleFunc("/call", func(w http.ResponseWriter, r *http.Request) {
		// Basic CORS for browser demos
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Auth-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Optional password auth for legacy HTTP signaling
		if cfg.AuthPassword != "" {
			ok := rtcAuthOK(r, cfg.AuthPassword)
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
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

	// Realtime WS signaling with trickle ICE (preferred for instant connects)
	mux.HandleFunc("/realtime", func(w http.ResponseWriter, r *http.Request) {
		// no CORS for WS; origin is validated by Upgrader.CheckOrigin (open for demo)
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		h.ServeWebSocket(w, r, cfg.ICEServersJSON, cfg.AuthPassword)
	})

	return &Server{Router: mux}
}

// rtcAuthOK validates Authorization/X-Auth-Token headers or ?password query against expected password.
func rtcAuthOK(r *http.Request, expected string) bool {
	if expected == "" || r == nil {
		return true
	}
	if q := r.URL.Query().Get("password"); q != "" && q == expected {
		return true
	}
	if h := r.Header.Get("X-Auth-Token"); h != "" && h == expected {
		return true
	}
	if ah := r.Header.Get("Authorization"); len(ah) > 7 {
		// Bearer <token>
		if ah[:7] == "Bearer " && ah[7:] == expected {
			return true
		}
	}
	return false
}
