package agent

import (
	"context"
	"time"
)

// Transcriber is the minimal interface for realtime STT.
// It must accept PCM 16kHz little-endian mono buffers and emit live and finalized text.
type Transcriber interface {
	Connect() error
	SendPCM16KLE(pcm []byte) error
	GetTranscripts() <-chan string
	Finalize() <-chan string
	// RecentlyDetectedVoice returns true if voice energy was seen within the given window.
	RecentlyDetectedVoice(window time.Duration) bool
	Close() error
}

// LLM is a minimal interface to generate a single response for a prompt.
type LLM interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

// TTS streams 48kHz PCM mono audio for the given text.
type TTS interface {
	StreamPCM48k(ctx context.Context, text string) (<-chan []byte, <-chan error)
}

// PCM48kSink consumes 48kHz PCM bytes and performs delivery (e.g., Opus encode to WebRTC).
// Implementations should buffer internally and pace delivery.
type PCM48kSink interface {
	WritePCM(pcm []byte)
	FlushTail()
	// Reset drops any queued frames immediately (used for barge-in).
	Reset()
}
