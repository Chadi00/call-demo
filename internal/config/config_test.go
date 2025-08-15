package config

import (
	"os"
	"testing"
)

func TestLoad_DefaultsAndEnv(t *testing.T) {
	os.Setenv("HTTP_ADDRESS", "")
	os.Setenv("ICE_SERVERS_JSON", "")
	os.Setenv("CEREBRAS_MODEL_ID", "")
	cfg := Load()
	if cfg.HTTPAddress == "" {
		t.Fatalf("expected default http address")
	}
	if cfg.ICEServersJSON == "" {
		t.Fatalf("expected default ice servers json")
	}
	if cfg.CerebrasModelID == "" {
		t.Fatalf("expected default cerebras model id")
	}
}
