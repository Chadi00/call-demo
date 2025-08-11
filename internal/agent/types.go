package agent

import (
	"context"
	"time"
)

type Transcriber interface {
	Connect() error
	SendPCM16KLE(pcm []byte) error
	GetTranscripts() <-chan string
	Finalize() <-chan string
	RecentlyDetectedVoice(window time.Duration) bool
	Close() error
}

type LLM interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

type TTS interface {
	StreamPCM48k(ctx context.Context, text string) (<-chan []byte, <-chan error)
}

type PCM48kSink interface {
	WritePCM(pcm []byte)
	FlushTail()
	Reset()
}
