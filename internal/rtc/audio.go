package rtc

import (
	"sync"
	"time"

	"github.com/hraban/opus"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

// OpusPacedWriter encodes incoming 48kHz PCM mono to Opus frames and writes them paced to a WebRTC track.
type OpusPacedWriter struct {
	enc          *opus.Encoder
	track        *webrtc.TrackLocalStaticSample
	pcmBuf       []int16
	frameSamples int
	frames       chan []byte
	stopCh       chan struct{}
	stopped      bool
	mu           sync.Mutex
}

// NewOpusPacedWriter constructs a paced writer with 20ms frames at 48kHz mono.
func NewOpusPacedWriter(track *webrtc.TrackLocalStaticSample) (*OpusPacedWriter, error) {
	enc, err := opus.NewEncoder(48000, 1, opus.AppVoIP)
	if err != nil {
		return nil, err
	}
	w := &OpusPacedWriter{
		enc:          enc,
		track:        track,
		frameSamples: 960, // 20ms at 48kHz
		frames:       make(chan []byte, 512),
		stopCh:       make(chan struct{}),
	}
	go w.pacer()
	return w, nil
}

// WritePCM buffers PCM 48kHz mono data and emits encoded Opus frames paced to the track.
func (w *OpusPacedWriter) WritePCM(pcmBytes []byte) {
	if len(pcmBytes) < 2 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// log buffer state
	// Note: keep logging minimal to avoid audio glitches
	// backlog := len(w.frames)
	// if backlog > cap(w.frames)/2 { log.Printf("writer backlog=%d/%d", backlog, cap(w.frames)) }
	// Convert bytes to int16 and append to pcmBuf
	need := len(pcmBytes) / 2
	startLen := len(w.pcmBuf)
	if cap(w.pcmBuf)-startLen < need {
		tmp := make([]int16, startLen, startLen+need+2048)
		copy(tmp, w.pcmBuf)
		w.pcmBuf = tmp
	}
	w.pcmBuf = w.pcmBuf[:startLen+need]
	for i := 0; i < need; i++ {
		w.pcmBuf[startLen+i] = int16(uint16(pcmBytes[2*i]) | uint16(pcmBytes[2*i+1])<<8)
	}

	// Encode full frames
	opusBuf := make([]byte, 4000)
	for len(w.pcmBuf) >= w.frameSamples {
		frame := w.pcmBuf[:w.frameSamples]
		n, _ := w.enc.Encode(frame, opusBuf)
		if n > 0 {
			pkt := make([]byte, n)
			copy(pkt, opusBuf[:n])
			w.pushFrame(pkt)
		}
		copy(w.pcmBuf, w.pcmBuf[w.frameSamples:])
		w.pcmBuf = w.pcmBuf[:len(w.pcmBuf)-w.frameSamples]
	}
}

// FlushTail pads the remaining PCM to a full frame and adds a short silence tail to avoid clipping.
func (w *OpusPacedWriter) FlushTail() {
	w.mu.Lock()
	opusBuf := make([]byte, 4000)
	if len(w.pcmBuf) > 0 {
		// zero-pad to full frame
		pad := make([]int16, w.frameSamples)
		copy(pad, w.pcmBuf)
		n, _ := w.enc.Encode(pad, opusBuf)
		if n > 0 {
			pkt := make([]byte, n)
			copy(pkt, opusBuf[:n])
			w.pushFrame(pkt)
		}
		w.pcmBuf = w.pcmBuf[:0]
	}
	w.mu.Unlock()
	// add ~200ms of silence (10 frames)
	silence := make([]int16, w.frameSamples)
	for i := 0; i < 10; i++ {
		n, _ := w.enc.Encode(silence, opusBuf)
		if n > 0 {
			pkt := make([]byte, n)
			copy(pkt, opusBuf[:n])
			w.pushFrame(pkt)
		}
	}
}

// Close stops the pacer.
func (w *OpusPacedWriter) Close() {
	w.mu.Lock()
	if !w.stopped {
		w.stopped = true
		close(w.stopCh)
	}
	w.mu.Unlock()
}

func (w *OpusPacedWriter) pacer() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			select {
			case frame := <-w.frames:
				_ = w.track.WriteSample(media.Sample{Data: frame, Duration: 20 * time.Millisecond})
			default:
			}
		}
	}
}

// pushFrame enqueues a frame, blocking until space is available or stopped.
func (w *OpusPacedWriter) pushFrame(pkt []byte) {
	for {
		select {
		case <-w.stopCh:
			return
		case w.frames <- pkt:
			return
		}
	}
}

// Reset clears any queued frames to support immediate barge-in.
func (w *OpusPacedWriter) Reset() {
	w.mu.Lock()
	// Drain frames channel quickly
	for {
		select {
		case <-w.frames:
		default:
			w.pcmBuf = w.pcmBuf[:0]
			w.mu.Unlock()
			return
		}
	}
}
