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
	ElevenLabsKey     string
	ElevenLabsVoiceID string
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
		cerebrasModel = "gpt-oss-120b"
	}
	if cerebrasKey == "" {
		log.Println("Warning: CEREBRAS_API_KEY not set - LLM will not work")
	}

	elevenKey := os.Getenv("ELEVENLABS_API_KEY")
	if elevenKey == "" {
		log.Println("Warning: ELEVENLABS_API_KEY not set - TTS will not work")
	}

	voiceID := os.Getenv("ELEVENLABS_VOICE_ID")
	if voiceID == "" {
		// Set a widely available default ElevenLabs voice id. If missing, TTS is disabled gracefully.
		// Common voice name "Rachel" requires an ID; leaving empty will log and skip TTS.
		log.Println("Warning: ELEVENLABS_VOICE_ID not set - using name 'Rachel' may fail; set a concrete voice ID from your ElevenLabs dashboard")
	}

	log.Printf("config: HTTP_ADDRESS=%s", addr)
	return Config{
		HTTPAddress:       addr,
		AssemblyAIKey:     assemblyAIKey,
		CerebrasKey:       cerebrasKey,
		CerebrasModelID:   cerebrasModel,
		ElevenLabsKey:     elevenKey,
		ElevenLabsVoiceID: voiceID,
	}
}
