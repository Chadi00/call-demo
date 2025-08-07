package config

import (
	"flag"
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Port                   string
	TwilioAccountSID       string
	TwilioAuthToken        string
	SupabaseURL            string
	SupabaseServiceRoleKey string
	SupabaseBucket         string
}

func Load() Config {
	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		log.Printf("No .env file found or error loading it: %v", err)
	}

	var cfg Config

	flag.StringVar(&cfg.Port, "port", getEnv("PORT", "8080"), "HTTP server port")
	flag.StringVar(&cfg.TwilioAccountSID, "twilio-sid", os.Getenv("TWILIO_ACCOUNT_SID"), "Twilio Account SID")
	flag.StringVar(&cfg.TwilioAuthToken, "twilio-token", os.Getenv("TWILIO_AUTH_TOKEN"), "Twilio Auth Token")
	flag.StringVar(&cfg.SupabaseURL, "supabase-url", os.Getenv("SUPABASE_URL"), "Supabase URL")
	flag.StringVar(&cfg.SupabaseServiceRoleKey, "supabase-key", os.Getenv("SUPABASE_SERVICE_ROLE_KEY"), "Supabase Service Role Key")
	flag.StringVar(&cfg.SupabaseBucket, "supabase-bucket", getEnv("SUPABASE_BUCKET", "voice-recording"), "Supabase Storage Bucket")
	flag.Parse()

	return cfg
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
