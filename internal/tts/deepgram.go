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

type DeepgramClient struct {
	apiKey     string
	model      string
	sampleRate int
	encoding   string
}

func NewDeepgramClient(apiKey, model string) *DeepgramClient {
	if model == "" {
		model = "aura-2-thalia-en"
	}
	return &DeepgramClient{apiKey: apiKey, model: model, sampleRate: 48000, encoding: "linear16"}
}

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
			return
		}

		options := &clientinterfaces.WSSpeakOptions{
			Model:      d.model,
			Encoding:   d.encoding,
			SampleRate: d.sampleRate,
		}

		var lastRecvUnix int64
		var seenAudio int32

		cb := &speakCallback{onBinary: func(data []byte) error {
			if len(data) == 0 {
				return nil
			}
			atomic.StoreInt64(&lastRecvUnix, time.Now().UnixNano())
			atomic.StoreInt32(&seenAudio, 1)
			b := make([]byte, len(data))
			copy(b, data)
			select {
			case pcmCh <- b:
			default:
			}
			return nil
		}}

		dg, err := speak.NewWSUsingCallback(ctx, d.apiKey, &clientinterfaces.ClientOptions{}, options, cb)
		if err != nil {
			errCh <- fmt.Errorf("deepgram: create ws client: %w", err)
			return
		}

		stopped := false
		stopClient := func() {
			if !stopped {
				stopped = true
				dg.Stop()
			}
		}
		defer stopClient()

		if ok := dg.Connect(); !ok {
			errCh <- fmt.Errorf("deepgram: connect failed")
			return
		}

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
			log.Printf("deepgram: flush error: %v", err)
		}

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
					stopClient()
					close(done)
					return
				}
			}
		}
	}()

	return pcmCh, errCh
}

type speakCallback struct{ onBinary func([]byte) error }

func (s *speakCallback) Open(*msginterfaces.OpenResponse) error         { return nil }
func (s *speakCallback) Metadata(*msginterfaces.MetadataResponse) error { return nil }
func (s *speakCallback) Flush(*msginterfaces.FlushedResponse) error     { return nil }
func (s *speakCallback) Clear(*msginterfaces.ClearedResponse) error     { return nil }
func (s *speakCallback) Close(*msginterfaces.CloseResponse) error       { return nil }
func (s *speakCallback) Warning(*msginterfaces.WarningResponse) error   { return nil }
func (s *speakCallback) Error(*msginterfaces.ErrorResponse) error       { return nil }
func (s *speakCallback) UnhandledEvent([]byte) error                    { return nil }
func (s *speakCallback) Binary(byMsg []byte) error {
	if s.onBinary != nil {
		return s.onBinary(byMsg)
	}
	return nil
}
