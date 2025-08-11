package tts

import (
    "context"
    "fmt"
    "log"
    "sync/atomic"
    "time"

    msginterfaces "github.com/deepgram/deepgram-go-sdk/pkg/api/speak/v1/websocket/interfaces"
    clientinterfaces "github.com/deepgram/deepgram-go-sdk/pkg/client/interfaces/v1"
    "github.com/deepgram/deepgram-go-sdk/pkg/client/speak"
)

// DeepgramClient streams TTS audio via Deepgram Speak WS API.
// It emits raw PCM little-endian 16-bit mono at 48kHz to match the Opus encoder pipeline.
type DeepgramClient struct {
    apiKey     string
    model      string
    sampleRate int
    encoding   string
}

// NewDeepgramClient constructs a Deepgram TTS client.
// model example: "aura-2-thalia-en". If empty, a default will be used.
func NewDeepgramClient(apiKey, model string) *DeepgramClient {
    if model == "" {
        model = "aura-2-thalia-en"
    }
    return &DeepgramClient{apiKey: apiKey, model: model, sampleRate: 48000, encoding: "linear16"}
}

// StreamPCM48k implements agent.TTS. It connects a Deepgram speak websocket, sends the text,
// and streams 48kHz PCM bytes via the returned channel until completion or context cancel.
func (d *DeepgramClient) StreamPCM48k(ctx context.Context, text string) (<-chan []byte, <-chan error) {
    pcmCh := make(chan []byte, 4096)
    errCh := make(chan error, 1)

    go func() {
        defer close(pcmCh)
        defer close(errCh)

        if d.apiKey == "" {
            errCh <- fmt.Errorf("deepgram: API key missing")
            return
        }
        if text == "" {
            // nothing to synthesize
            return
        }

        // Initialize options
        options := &clientinterfaces.WSSpeakOptions{
            Model:      d.model,
            Encoding:   d.encoding,
            SampleRate: d.sampleRate,
        }

        // Track activity to determine when stream completes
        var lastRecvUnix int64
        var seenAudio int32

        // Implement callback
        cb := &speakCallback{onBinary: func(data []byte) error {
            if len(data) == 0 {
                return nil
            }
            atomic.StoreInt64(&lastRecvUnix, time.Now().UnixNano())
            atomic.StoreInt32(&seenAudio, 1)
            b := make([]byte, len(data))
            copy(b, data)
            select { case pcmCh <- b: default: }
            return nil
        }}

        // Create WS client using API key auth and our callback
        dg, err := speak.NewWSUsingCallback(ctx, d.apiKey, &clientinterfaces.ClientOptions{}, options, cb)
        if err != nil {
            errCh <- fmt.Errorf("deepgram: create ws client: %w", err)
            return
        }

        // Ensure cleanup on exit
        stopped := false
        stopClient := func() {
            if !stopped {
                stopped = true
                dg.Stop()
            }
        }
        defer stopClient()

        // Connect and stream text
        if ok := dg.Connect(); !ok {
            errCh <- fmt.Errorf("deepgram: connect failed")
            return
        }

        // If the context is canceled (barge-in), stop the stream promptly
        done := make(chan struct{})
        go func() {
            select {
            case <-ctx.Done():
                stopClient()
            case <-done:
            }
        }()

        if err := dg.SpeakWithText(text); err != nil {
            errCh <- fmt.Errorf("deepgram: speak text: %w", err)
            close(done)
            return
        }
        if err := dg.Flush(); err != nil {
            // not fatal; log and continue
            log.Printf("deepgram: flush error: %v", err)
        }

        // Wait until no audio activity for a short window, then stop
        idleWindow := 400 * time.Millisecond
        ticker := time.NewTicker(50 * time.Millisecond)
        defer ticker.Stop()
        deadline := time.Now().Add(12 * time.Second)
        for {
            select {
            case <-ctx.Done():
                stopClient()
                close(done)
                return
            case <-ticker.C:
                if atomic.LoadInt32(&seenAudio) == 1 {
                    last := time.Unix(0, atomic.LoadInt64(&lastRecvUnix))
                    if !last.IsZero() && time.Since(last) > idleWindow {
                        stopClient()
                        close(done)
                        return
                    }
                }
                if time.Now().After(deadline) {
                    // safety timeout
                    stopClient()
                    close(done)
                    return
                }
            }
        }
    }()

    return pcmCh, errCh
}

// speakCallback implements Deepgram's SpeakMessageCallback and forwards audio to a function.
type speakCallback struct{ onBinary func([]byte) error }

func (s *speakCallback) Open(*msginterfaces.OpenResponse) error             { return nil }
func (s *speakCallback) Metadata(*msginterfaces.MetadataResponse) error     { return nil }
func (s *speakCallback) Flush(*msginterfaces.FlushedResponse) error         { return nil }
func (s *speakCallback) Clear(*msginterfaces.ClearedResponse) error         { return nil }
func (s *speakCallback) Close(*msginterfaces.CloseResponse) error           { return nil }
func (s *speakCallback) Warning(*msginterfaces.WarningResponse) error       { return nil }
func (s *speakCallback) Error(*msginterfaces.ErrorResponse) error           { return nil }
func (s *speakCallback) UnhandledEvent([]byte) error                        { return nil }
func (s *speakCallback) Binary(byMsg []byte) error { if s.onBinary != nil { return s.onBinary(byMsg) } ; return nil }


