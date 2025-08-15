package barge

import (
	"encoding/binary"
	"math"
	"strings"
	"sync"
	"time"
)

// lightweight DSP stubs to keep this self-contained and testable.
// In production, wire CGO for WebRTC AEC3/RNNoise and real DTD.

type simpleAEC struct {
	refRing *circularPCM
}

func newSimpleAEC(sr int) *simpleAEC { return &simpleAEC{refRing: newCircularPCM(2000, sr)} }

// feedRef accepts 10ms reference at engine sample rate
func (s *simpleAEC) feedRef(frame Frame10ms) { s.refRing.Write(frame) }

// process subtracts a crude scaled correlation from near; this is a placeholder.
func (s *simpleAEC) process(near Frame10ms) Frame10ms {
	// no real AEC: pass-through; production should implement proper AEC.
	out := make([]int16, len(near))
	copy(out, near)
	return Frame10ms(out)
}

type simpleVAD struct {
	threshold float64
	smoothN   int
	win       []bool
}

func newSimpleVAD() *simpleVAD { return &simpleVAD{threshold: 300.0, smoothN: 4} }

func (v *simpleVAD) isSpeech(frame Frame10ms) bool {
	if len(frame) == 0 {
		return false
	}
	var sum float64
	for _, s := range frame {
		f := float64(s)
		sum += f * f
	}
	rms := math.Sqrt(sum / float64(len(frame)))
	b := rms >= v.threshold
	v.win = append(v.win, b)
	if len(v.win) > v.smoothN {
		v.win = v.win[len(v.win)-v.smoothN:]
	}
	trueCount := 0
	for _, x := range v.win {
		if x {
			trueCount++
		}
	}
	return trueCount*2 >= len(v.win)
}

type simpleDTD struct {
	// Placeholder: track residual energy vs ref energy
	lastOverlap bool
}

func (d *simpleDTD) overlap(residualWin []Frame10ms, _ []Frame10ms) bool {
	// Simplified heuristic: consider overlap if residual energy is high.
	var sum float64
	var n int
	for _, f := range residualWin {
		for _, s := range f {
			x := float64(s)
			sum += x * x
			n++
		}
	}
	if n == 0 {
		return false
	}
	rms := math.Sqrt(sum / float64(n))
	d.lastOverlap = rms > 500.0
	return d.lastOverlap
}

// circularPCM stores 16-bit PCM samples for pre-roll and ref ring.
type circularPCM struct {
	mu       sync.Mutex
	buf      []int16
	cap      int
	writePos int
	sr       int
}

func newCircularPCM(capacityMs int, sampleRate int) *circularPCM {
	samples := capacityMs * sampleRate / 1000
	if samples < sampleRate/10 {
		samples = sampleRate / 10
	}
	return &circularPCM{buf: make([]int16, samples), cap: samples, sr: sampleRate}
}

func (c *circularPCM) Write(frame Frame10ms) {
	c.mu.Lock()
	for _, s := range frame {
		c.buf[c.writePos] = s
		c.writePos = (c.writePos + 1) % c.cap
	}
	c.mu.Unlock()
}

func (c *circularPCM) ReadLastMs(ms int) []int16 {
	c.mu.Lock()
	n := ms * c.sr / 1000
	if n > c.cap {
		n = c.cap
	}
	out := make([]int16, n)
	start := (c.writePos - n + c.cap) % c.cap
	for i := 0; i < n; i++ {
		out[i] = c.buf[(start+i)%c.cap]
	}
	c.mu.Unlock()
	return out
}

func (c *circularPCM) ZeroLastMs(ms int) {
	c.mu.Lock()
	n := ms * c.sr / 1000
	if n > c.cap {
		n = c.cap
	}
	for i := 0; i < n; i++ {
		idx := (c.writePos - 1 - i + c.cap) % c.cap
		c.buf[idx] = 0
	}
	c.mu.Unlock()
}

type voteWindow struct {
	winDur time.Duration
	hist   []bool
	mu     sync.Mutex
}

func newVoteWindow(ms int) *voteWindow {
	return &voteWindow{winDur: time.Duration(ms) * time.Millisecond}
}

func (v *voteWindow) Push(b bool) {
	v.mu.Lock()
	v.hist = append(v.hist, b)
	max := int(v.winDur/(10*time.Millisecond)) + 1
	if len(v.hist) > max {
		v.hist = v.hist[len(v.hist)-max:]
	}
	v.mu.Unlock()
}

func (v *voteWindow) Ratio() float64 {
	v.mu.Lock()
	if len(v.hist) == 0 {
		v.mu.Unlock()
		return 0
	}
	var t int
	for _, b := range v.hist {
		if b {
			t++
		}
	}
	r := float64(t) / float64(len(v.hist))
	v.mu.Unlock()
	return r
}

func (v *voteWindow) Reset() {
	v.mu.Lock()
	v.hist = v.hist[:0]
	v.mu.Unlock()
}

// fixed-size 10ms frame window of latest N frames
type frameWindow struct {
	mu     sync.Mutex
	frames []Frame10ms
	size   int
}

func newFrameWindow(n int) *frameWindow { return &frameWindow{size: n} }

func (w *frameWindow) Push(f Frame10ms) {
	w.mu.Lock()
	w.frames = append(w.frames, f)
	if len(w.frames) > w.size {
		w.frames = w.frames[len(w.frames)-w.size:]
	}
	w.mu.Unlock()
}

func (w *frameWindow) Snapshot() []Frame10ms {
	w.mu.Lock()
	cp := make([]Frame10ms, len(w.frames))
	copy(cp, w.frames)
	w.mu.Unlock()
	return cp
}

// Bloom filter for TTS text discount (very small, simple)
type bloom struct{ bits []byte }

func newBloom(n int) *bloom { return &bloom{bits: make([]byte, n)} }

func (b *bloom) hash(s string) int {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return int(h) % len(b.bits)
}
func (b *bloom) Add(s string) {
	if len(b.bits) > 0 {
		b.bits[b.hash(s)] = 1
	}
}
func (b *bloom) Contains(s string) bool { return len(b.bits) > 0 && b.bits[b.hash(s)] == 1 }

// EngineImpl implements Engine.
type EngineImpl struct {
	cfg Config
	ev  Events

	speaking bool

	aec      *simpleAEC
	vad      *simpleVAD
	dtd      *simpleDTD
	micWin   *frameWindow
	refWin   *frameWindow
	ttsRef   *circularPCM
	preRoll  *circularPCM
	votesOn  *voteWindow
	votesOff *voteWindow
	ttsBloom *bloom

	// partial handling
	lastPartial string
	lastTokens  []string

	mu sync.Mutex
}

func NewEngine(cfg Config, ev Events) *EngineImpl {
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 16000
	}
	e := &EngineImpl{
		cfg:      cfg,
		ev:       ev,
		aec:      newSimpleAEC(cfg.SampleRate),
		vad:      newSimpleVAD(),
		dtd:      &simpleDTD{},
		micWin:   newFrameWindow(16), // ~160ms
		refWin:   newFrameWindow(16),
		ttsRef:   newCircularPCM(2000, cfg.SampleRate),
		preRoll:  newCircularPCM(300, cfg.SampleRate),
		votesOn:  newVoteWindow(cfg.FuseWinMs),
		votesOff: newVoteWindow(cfg.HysteresisOffMs),
		ttsBloom: newBloom(4096),
	}
	return e
}

func (e *EngineImpl) Reset() {
	e.mu.Lock()
	e.votesOn.Reset()
	e.votesOff.Reset()
	e.lastPartial = ""
	e.lastTokens = nil
	e.mu.Unlock()
}

func (e *EngineImpl) SetSpeaking(on bool) { e.mu.Lock(); e.speaking = on; e.mu.Unlock() }

// Feed 16kHz PCM16LE mic audio (arbitrary length). The engine will segment into 10ms frames.
func (e *EngineImpl) FeedMic16k(pcm []byte) {
	if len(pcm) < 2 {
		return
	}
	// split into 10ms frames at cfg.SampleRate
	samplesPer10ms := e.cfg.SampleRate / 100
	for off := 0; off+samplesPer10ms*2 <= len(pcm); off += samplesPer10ms * 2 {
		frame := make([]int16, samplesPer10ms)
		for i := 0; i < samplesPer10ms; i++ {
			frame[i] = int16(binary.LittleEndian.Uint16(pcm[off+i*2 : off+i*2+2]))
		}
		e.onMicFrame(Frame10ms(frame))
	}
}

// Feed 48kHz PCM16LE TTS reference audio; we downsample to cfg.SampleRate (simple decimation by factor 3 for 48k->16k).
func (e *EngineImpl) FeedTTS48k(pcm []byte) {
	if len(pcm) < 2 {
		return
	}
	// decimate by 3 if SampleRate==16k; otherwise, naive resample omitted for brevity.
	if e.cfg.SampleRate == 16000 {
		// gather into 10ms @48k (480 samples) then decimate to 160 samples
		samplesPer10ms48k := 480
		for off := 0; off+samplesPer10ms48k*2 <= len(pcm); off += samplesPer10ms48k * 2 {
			ref48 := make([]int16, samplesPer10ms48k)
			for i := 0; i < samplesPer10ms48k; i++ {
				ref48[i] = int16(binary.LittleEndian.Uint16(pcm[off+i*2 : off+i*2+2]))
			}
			// decimate: take every 3rd sample
			ref16 := make([]int16, samplesPer10ms48k/3)
			for i := 0; i < len(ref16); i++ {
				ref16[i] = ref48[i*3]
			}
			e.aec.feedRef(Frame10ms(ref16))
			e.ttsRef.Write(Frame10ms(ref16))
			e.refWin.Push(Frame10ms(ref16))
		}
	}
}

// NotifyPartial supplies running transcript text. Engine will derive token growth vs previous partial.
func (e *EngineImpl) NotifyPartial(text string) {
	e.mu.Lock()
	e.lastPartial = text
	e.mu.Unlock()
}

// NotifyTTSText lets engine discount echoed words while TTS is speaking.
func (e *EngineImpl) NotifyTTSText(text string) {
	fields := strings.Fields(strings.ToLower(text))
	for _, w := range fields {
		e.ttsBloom.Add(w)
	}
}

func (e *EngineImpl) StartSpeaking(_ interface{}, _ <-chan string) {}
func (e *EngineImpl) CancelSpeaking()                              {}

// onMicFrame runs per 10ms frame.
func (e *EngineImpl) onMicFrame(frame Frame10ms) {
	e.mu.Lock()
	speaking := e.speaking
	e.mu.Unlock()
	// run AEC (stub), VAD, DTD windows, ASR growth from lastPartial
	residual := e.aec.process(frame)
	e.preRoll.Write(residual)
	e.micWin.Push(residual)

	vadYes := e.vad.isSpeech(residual)
	dtdYes := e.dtd.overlap(e.micWin.Snapshot(), e.refWin.Snapshot())
	asrYes := e.asrGrowth()

	vote := 0
	if vadYes {
		vote++
	}
	if asrYes {
		vote++
	}
	if dtdYes {
		vote++
	}

	if speaking {
		e.votesOn.Push(vote >= 2)
		e.votesOff.Push(vote == 0)
		if e.votesOn.Ratio() >= 2.0/3.0 {
			e.trigger()
			return
		}
		if e.votesOff.Ratio() >= 2.0/3.0 {
			e.votesOn.Reset()
		}
	}
}

func (e *EngineImpl) asrGrowth() bool {
	e.mu.Lock()
	text := e.lastPartial
	e.mu.Unlock()
	if strings.TrimSpace(text) == "" {
		return false
	}
	tokens := strings.Fields(strings.ToLower(text))
	if len(tokens) == 0 {
		return false
	}
	// count new tokens vs e.lastTokens
	newCount := 0
	maxPrev := len(e.lastTokens)
	for i := maxPrev; i < len(tokens); i++ {
		w := tokens[i]
		if isStopword(w) {
			continue
		}
		if e.ttsBloom.Contains(w) {
			continue
		}
		newCount++
		if newCount >= e.cfg.ASRTokens {
			e.lastTokens = tokens
			return true
		}
	}
	e.lastTokens = tokens
	return false
}

func (e *EngineImpl) trigger() {
	// zero last 300ms in TTS ref to reduce AEC confusion
	e.ttsRef.ZeroLastMs(300)
	// collect pre-roll and export as bytes
	pre := e.preRoll.ReadLastMs(e.cfg.PreRollMs)
	preBytes := make([]byte, len(pre)*2)
	for i, s := range pre {
		binary.LittleEndian.PutUint16(preBytes[i*2:(i+1)*2], uint16(s))
	}
	if e.ev.OnTTSStop != nil {
		e.ev.OnTTSStop(time.Now())
	}
	if e.ev.OnTrigger != nil {
		e.ev.OnTrigger(time.Now(), Cues{VAD: true, ASR: true, DTD: true}, preBytes)
	}
	e.votesOn.Reset()
	e.votesOff.Reset()
}

func isStopword(s string) bool {
	switch s {
	case "the", "a", "an", "and", "or", "to", "of", "in", "on", "for", "is", "it", "uh", "um":
		return true
	}
	return false
}
