package barge

import (
	"context"
	"time"
)

// Frame10ms represents a 10ms mono PCM frame at SampleRate Hz.
// For 16kHz mono, this is 160 samples of int16.
type Frame10ms []int16

// Config holds the thresholds for the barge-in fusion engine.
type Config struct {
	ResidualVadMs   int     // 120–180
	ASRTokens       int     // 2–3
	ASRConf         float32 // kept for compatibility; not all ASR provide conf in partials
	DTDOverlapMs    int     // 100–150
	FuseWinMs       int     // 150–180
	HysteresisOffMs int     // 200
	PreRollMs       int     // 200–250
	SampleRate      int     // 16000 or 8000 (engine expects 10ms frames at this rate)
}

// Cues indicates which detectors voted true in a window.
type Cues struct{ VAD, ASR, DTD bool }

// Events allows host to react to barge-in.
type Events struct {
	// OnTrigger fires when fusion crosses threshold; preRoll contains the last PreRollMs
	// of residual audio (cleaned by AEC) as PCM16LE at SampleRate.
	OnTrigger func(ts time.Time, cues Cues, preRoll []byte)
	// OnTTSStop should stop TTS output immediately (e.g., cancel synthesis and mute output device).
	OnTTSStop func(ts time.Time)
}

// Engine is the barge-in fusion orchestrator.
type Engine interface {
	// FeedMic16k feeds arbitrary-length PCM16LE at SampleRate (16kHz typical). The engine will split to 10ms frames.
	FeedMic16k(pcm []byte)
	// FeedTTS48k feeds PCM16LE 48kHz mono of the TTS reference; it is downsampled to SampleRate for AEC reference.
	FeedTTS48k(pcm []byte)
	// NotifyPartial provides the latest running transcript partial (string-level). Engine derives token growth.
	NotifyPartial(text string)
	// NotifyTTSText lets the engine discount echoed content (Bloom of current spoken text).
	NotifyTTSText(text string)
	// SetSpeaking toggles speaking state; detection triggers only when speaking is true.
	SetSpeaking(on bool)
	// StartSpeaking is optional convenience for designs where engine manages TTS end-to-end.
	StartSpeaking(ctx context.Context, textStream <-chan string)
	// CancelSpeaking cancels current speaking session (noop if unmanaged).
	CancelSpeaking()
	// Reset clears window state and pre-roll.
	Reset()
}

// Profile presets.
func DefaultWebRTCHeadset() Config {
	return Config{
		ResidualVadMs:   120,
		ASRTokens:       3,
		ASRConf:         0.6,
		DTDOverlapMs:    100,
		FuseWinMs:       150,
		HysteresisOffMs: 200,
		PreRollMs:       220,
		SampleRate:      16000,
	}
}
