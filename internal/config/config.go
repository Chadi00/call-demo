package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config holds application configuration.
type Config struct {
	HTTPAddress       string
	AssemblyAIKey     string
	CerebrasKey       string
	CerebrasModelID   string
    DeepgramKey       string
    DeepgramTTSModel  string
	// AuthPassword secures realtime WS signaling; if empty, auth is disabled
	AuthPassword string
	// ICEServersJSON is a JSON array of ICE server objects compatible with WebRTC
	// example: [{"urls":["stun:stun.l.google.com:19302"]}] or TURN entries with username/credential
	ICEServersJSON string
}

// Load reads environment variables and returns Config with sane defaults.
func Load() Config {
	err := godotenv.Load()
	if err != nil {
		log.Println("Error loading .env file")
	}

	addr := os.Getenv("HTTP_ADDRESS")
	if addr == "" {
		addr = ":8080"
	}

	assemblyAIKey := os.Getenv("ASSEMBLYAI_API_KEY")
	if assemblyAIKey == "" {
		log.Println("Warning: ASSEMBLYAI_API_KEY not set - transcription will not work")
	}

	cerebrasKey := os.Getenv("CEREBRAS_API_KEY")
	cerebrasModel := os.Getenv("CEREBRAS_MODEL_ID")
	if cerebrasModel == "" {
		cerebrasModel = "llama-4-maverick-17b-128e-instruct"
	}
	if cerebrasKey == "" {
		log.Println("Warning: CEREBRAS_API_KEY not set - LLM will not work")
	}

    deepgramKey := os.Getenv("DEEPGRAM_API_KEY")
    if deepgramKey == "" {
        log.Println("Warning: DEEPGRAM_API_KEY not set - TTS will not work")
    }
    deepgramModel := os.Getenv("DEEPGRAM_TTS_MODEL")
    if deepgramModel == "" {
        deepgramModel = "aura-2-thalia-en"
    }

	authPassword := os.Getenv("AUTH_PASSWORD")
	if authPassword == "" {
		log.Println("Warning: AUTH_PASSWORD not set - realtime WS signaling will be unauthenticated")
	}

	iceServersJSON := os.Getenv("ICE_SERVERS_JSON")
	if iceServersJSON == "" {
		// default to public Google STUN
		iceServersJSON = `[{"urls":["stun:stun.l.google.com:19302"]}]`
	}

	log.Printf("config: HTTP_ADDRESS=%s", addr)
	return Config{
		HTTPAddress:       addr,
		AssemblyAIKey:     assemblyAIKey,
		CerebrasKey:       cerebrasKey,
		CerebrasModelID:   cerebrasModel,
        DeepgramKey:       deepgramKey,
        DeepgramTTSModel:  deepgramModel,
		AuthPassword:      authPassword,
		ICEServersJSON:    iceServersJSON,
	}
}
