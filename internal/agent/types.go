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

// BargeEngine is a minimal interface the session uses to cooperate with
// a barge-in detector. It avoids coupling to implementation details.
type BargeEngine interface {
	// NotifyPartial provides latest running transcript text.
	NotifyPartial(text string)
	// FeedTTS48k feeds 48kHz PCM16LE frames that are being sent to the user.
	FeedTTS48k(pcm []byte)
	// NotifyTTSText provides the text currently being spoken so the engine can
	// discount echo in its ASR growth heuristic.
	NotifyTTSText(text string)
	// SetSpeaking toggles whether the agent is currently speaking.
	SetSpeaking(on bool)
}
