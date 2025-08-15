package tts

import (
	"context"
	"testing"
	"time"
)

// This is a smoke test for StreamPCM48k without an API key; it should error quickly
func TestDeepgram_StreamPCM48k_NoKey(t *testing.T) {
	d := NewDeepgramClient("", "")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	pcmCh, errCh := d.StreamPCM48k(ctx, "hello")
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("expected error when api key missing")
		}
	case <-pcmCh:
		// ignore
	case <-time.After(300 * time.Millisecond):
		t.Fatalf("timeout waiting for error")
	}
}
