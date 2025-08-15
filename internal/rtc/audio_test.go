package rtc

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v3/pkg/media"
)

type fakeTrack struct{ writes int32 }

func (f *fakeTrack) WriteSample(s media.Sample) error {
	atomic.AddInt32(&f.writes, 1)
	return nil
}

func TestOpusPacedWriter_PacerWritesFrames(t *testing.T) {
	ft := &fakeTrack{}
	w := &OpusPacedWriter{
		enc:          nil, // encoder not needed for this test
		track:        ft,
		frameSamples: 960,
		frames:       make(chan []byte, 8),
		stopCh:       make(chan struct{}),
	}
	// Start pacer
	done := make(chan struct{})
	go func() { w.pacer(); close(done) }()

	// Push a few frames into the queue
	for i := 0; i < 3; i++ {
		w.pushFrame([]byte{0x01, 0x02})
	}

	// Allow pacer to tick and drain
	time.Sleep(50 * time.Millisecond)
	close(w.stopCh)
	<-done

	if atomic.LoadInt32(&ft.writes) == 0 {
		t.Fatalf("expected pacer to write at least one frame")
	}
}

func TestOpusPacedWriter_ResetDrains(t *testing.T) {
	ft := &fakeTrack{}
	w := &OpusPacedWriter{
		enc:          nil,
		track:        ft,
		frameSamples: 960,
		frames:       make(chan []byte, 8),
		stopCh:       make(chan struct{}),
		pcmBuf:       []int16{1, 2, 3},
	}
	// Seed frames channel
	w.frames <- []byte{0x01}
	w.frames <- []byte{0x02}
	w.Reset()
	select {
	case <-w.frames:
		t.Fatalf("expected frames channel to be drained")
	default:
	}
	if len(w.pcmBuf) != 0 {
		t.Fatalf("expected pcmBuf to be reset, got len=%d", len(w.pcmBuf))
	}
}
