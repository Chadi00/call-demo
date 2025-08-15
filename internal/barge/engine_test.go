package barge

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

func pcmSine(sr int, hz float64, durMs int) []byte {
	n := sr * durMs / 1000
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(8000 * math.Sin(2*math.Pi*hz*float64(i)/float64(sr)))
		binary.LittleEndian.PutUint16(out[i*2:(i+1)*2], uint16(v))
	}
	return out
}

func TestEngine_TriggersOnSpeechDuringSpeaking(t *testing.T) {
	cfg := DefaultWebRTCHeadset()
	triggered := false
	stopped := false
	e := NewEngine(cfg, Events{
		OnTTSStop: func(ts time.Time) { stopped = true },
		OnTrigger: func(ts time.Time, cues Cues, pre []byte) { triggered = true },
	})
	e.SetSpeaking(true)
	// feed TTS ref for 300ms (48k sine) then user speech at 16k
	tts := pcmSine(48000, 440, 200)
	e.FeedTTS48k(tts)
	// simulate ASR partial growth
	go func() {
		e.NotifyPartial("hello there")
		time.Sleep(80 * time.Millisecond)
		e.NotifyPartial("hello there assistant")
	}()
	// feed mic speech for 400ms
	mic := pcmSine(16000, 220, 400)
	e.FeedMic16k(mic)
	if !triggered {
		t.Fatalf("expected trigger true")
	}
	if !stopped {
		t.Fatalf("expected stop true")
	}
}
