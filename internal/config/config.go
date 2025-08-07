package config

import "os"

// Config holds all environment-driven configuration for the service.
type Config struct {
	// Server
	Port    string
	BaseURL string

	// Twilio
	TwilioAccountSID string
	TwilioAuthToken  string

	// Supabase
	SupabaseURL            string
	SupabaseServiceRoleKey string
	SupabaseBucket         string

	// Demo destination number for conversation endpoint
	DestinationNumber string
}

// Load reads configuration from environment variables and applies defaults.
func Load() Config {
	cfg := Config{
		Port:                   getEnv("PORT", "8080"),
		BaseURL:                os.Getenv("BASE_URL"),
		TwilioAccountSID:       os.Getenv("TWILIO_ACCOUNT_SID"),
		TwilioAuthToken:        os.Getenv("TWILIO_AUTH_TOKEN"),
		SupabaseURL:            os.Getenv("SUPABASE_URL"),
		SupabaseServiceRoleKey: os.Getenv("SUPABASE_SERVICE_ROLE_KEY"),
		SupabaseBucket:         getEnv("SUPABASE_BUCKET", "voice-recording"),
		DestinationNumber:      os.Getenv("DESTINATION_NUMBER"),
	}
	return cfg
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
