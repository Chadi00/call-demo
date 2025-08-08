package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
)

// ElevenLabsClient realtime TTS client over WebSocket stream-input.
type ElevenLabsClient struct {
	APIKey  string
	VoiceID string
}

func NewElevenLabsClient(apiKey, voiceID string) *ElevenLabsClient {
	return &ElevenLabsClient{APIKey: apiKey, VoiceID: voiceID}
}

// ws structures are intentionally removed while HTTP streaming is used for reliability

// StreamPCM opens a WS session and streams PCM_48000 audio for the given text.
// Note: This uses ElevenLabs stream-input WS; audio is returned as binary frames.
func (e *ElevenLabsClient) StreamPCM(ctx context.Context, text string) (<-chan []byte, <-chan error) {
	pcmCh := make(chan []byte, 4096)
	errCh := make(chan error, 1)
	go func() {
		defer close(pcmCh)
		defer close(errCh)
		if e.APIKey == "" || e.VoiceID == "" {
			errCh <- fmt.Errorf("elevenlabs ws: api key or voice id missing")
			return
		}
		// Temporarily force HTTP streaming for reliability
		log.Printf("elevenlabs: using HTTP streaming for TTS")
		if err := httpStream(ctx, e, text, pcmCh); err != nil {
			errCh <- err
			return
		}
	}()
	return pcmCh, errCh
}

// StreamPCM48k adapts to the agent.TTS interface.
func (e *ElevenLabsClient) StreamPCM48k(ctx context.Context, text string) (<-chan []byte, <-chan error) {
	return e.StreamPCM(ctx, text)
}

// wsStream tries the ElevenLabs WebSocket stream-input and returns nil when audio was produced or context closed.
// If it returns a non-nil error, caller may fallback to HTTP.
// wsStream removed while WS impl is under review; HTTP streaming is used instead

// httpStream streams PCM audio via HTTP streaming endpoint as a fallback.
func httpStream(ctx context.Context, e *ElevenLabsClient, text string, pcmCh chan<- []byte) error {
	u := url.URL{
		Scheme: "https",
		Host:   "api.elevenlabs.io",
		Path:   "/v1/text-to-speech/" + e.VoiceID + "/stream",
	}
	q := u.Query()
	q.Set("model_id", "eleven_flash_v2_5")
	q.Set("output_format", "pcm_48000")
	// lower streaming latency target (0..4 where lower is lower latency, may trade quality)
	q.Set("optimize_streaming_latency", "2")
	u.RawQuery = q.Encode()

	body := map[string]any{
		"model_id": "eleven_flash_v2_5",
		"text":     text,
		"voice_settings": map[string]any{
			"stability":         0.4,
			"similarity_boost":  0.7,
			"style":             0.0,
			"use_speaker_boost": true,
		},
		// use shorter chunks to reduce tail cutoff; server still streams
		"generation_config": map[string]any{
			"chunk_length_schedule": []int{80, 120, 160, 200},
		},
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("xi-api-key", e.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("elevenlabs http stream error: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("elevenlabs http status=%d body=%s", resp.StatusCode, string(b))
	}

	// Stream body chunks
	bufChunk := make([]byte, 4096)
	logged := false
	for {
		n, rerr := resp.Body.Read(bufChunk)
		if n > 0 {
			if !logged {
				log.Printf("elevenlabs http: receiving audio stream (%d bytes first chunk)", n)
				logged = true
			}
			out := make([]byte, n)
			copy(out, bufChunk[:n])
			select {
			case pcmCh <- out:
			default:
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			return fmt.Errorf("elevenlabs http read error: %w", rerr)
		}
	}
}
