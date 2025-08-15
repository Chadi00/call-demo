package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeTranscriber struct {
	transcripts chan string
	finals      chan string
}

func (f *fakeTranscriber) Connect() error                                  { return nil }
func (f *fakeTranscriber) SendPCM16KLE(pcm []byte) error                   { return nil }
func (f *fakeTranscriber) GetTranscripts() <-chan string                   { return f.transcripts }
func (f *fakeTranscriber) Finalize() <-chan string                         { return f.finals }
func (f *fakeTranscriber) RecentlyDetectedVoice(window time.Duration) bool { return false }
func (f *fakeTranscriber) Close() error                                    { close(f.transcripts); close(f.finals); return nil }

type fakeLLM struct {
	reply string
	err   error
}

func (f fakeLLM) Generate(ctx context.Context, prompt string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}

type fakeTTS struct{ frames int32 }

func (f *fakeTTS) StreamPCM48k(ctx context.Context, text string) (<-chan []byte, <-chan error) {
	pcm := make(chan []byte, 10)
	errc := make(chan error, 1)
	go func() {
		defer close(pcm)
		defer close(errc)
		// emit a few small PCM chunks
		for i := 0; i < 3; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			pcm <- []byte{1, 0, 2, 0}
			atomic.AddInt32(&f.frames, 1)
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return pcm, errc
}

type fakeSink struct{ wrote int32 }

func (s *fakeSink) WritePCM(p []byte) { atomic.AddInt32(&s.wrote, 1) }
func (*fakeSink) FlushTail()          {}
func (*fakeSink) Reset()              {}

func TestSession_AddsOnlySpokenTextToHistory(t *testing.T) {
	tr := &fakeTranscriber{transcripts: make(chan string, 10), finals: make(chan string, 10)}
	llm := fakeLLM{reply: "Hello world. This will be interrupted."}
	tts := &fakeTTS{}
	sink := &fakeSink{}
	sess := NewSession(tr, llm, tts, sink, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := sess.Start(ctx)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer stop()

	// Finalize one utterance
	tr.finals <- "hi"
	// Wait until at least one TTS frame has been produced, then barge to simulate interruption
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) && atomic.LoadInt32(&tts.frames) == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	sess.BargeIn()
	time.Sleep(20 * time.Millisecond)

	// History must have user + possibly partial assistant; but assistant should be only whatever was spoken
	sess.mu.Lock()
	var userCount, assistantCount int
	for _, h := range sess.history {
		if h.Role == "USER" {
			userCount++
		}
		if h.Role == "ASSISTANT" {
			assistantCount++
		}
	}
	sess.mu.Unlock()
	if userCount == 0 {
		t.Fatalf("expected user turn recorded")
	}
	if assistantCount > 1 {
		t.Fatalf("expected at most one assistant entry, got %d", assistantCount)
	}
}

func TestSession_SkipsAssistantWhenNothingSpoken(t *testing.T) {
	tr := &fakeTranscriber{transcripts: make(chan string, 10), finals: make(chan string, 10)}
	llm := fakeLLM{reply: "Hello"}
	tts := &fakeTTS{}
	sink := &fakeSink{}
	sess := NewSession(tr, llm, tts, sink, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := sess.Start(ctx)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer stop()

	tr.finals <- "hi"
	// Immediately barge before first audio frame likely delivered
	sess.BargeIn()
	time.Sleep(30 * time.Millisecond)
	sess.mu.Lock()
	var assistantCount int
	for _, h := range sess.history {
		if h.Role == "ASSISTANT" {
			assistantCount++
		}
	}
	wrote := atomic.LoadInt32(&sink.wrote)
	sess.mu.Unlock()
	if wrote == 0 && assistantCount != 0 {
		t.Fatalf("expected 0 assistant entries when no audio written, got %d", assistantCount)
	}
}

// negative test on LLM error path
func TestSession_NoAppendOnLLMError(t *testing.T) {
	tr := &fakeTranscriber{transcripts: make(chan string, 10), finals: make(chan string, 10)}
	llm := fakeLLM{err: errors.New("boom")}
	tts := &fakeTTS{}
	sink := &fakeSink{}
	sess := NewSession(tr, llm, tts, sink, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := sess.Start(ctx)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer stop()
	tr.finals <- "hi"
	time.Sleep(30 * time.Millisecond)
	sess.mu.Lock()
	defer sess.mu.Unlock()
	var assistantCount int
	for _, h := range sess.history {
		if h.Role == "ASSISTANT" {
			assistantCount++
		}
	}
	if assistantCount != 0 {
		t.Fatalf("expected 0 assistant entries on LLM error, got %d", assistantCount)
	}
}

func TestChunkReply_SplitsAndTrims(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"  Hello world.  How are you?\nI am fine!  ", []string{"Hello world.", "How are you?", "I am fine!"}},
		{"no punctuation here", []string{"no punctuation here"}},
		{"", nil},
	}
	for _, tc := range cases {
		got := chunkReply(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("len mismatch for %q: got %d want %d", tc.in, len(got), len(tc.want))
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("elem %d mismatch: got %q want %q", i, got[i], tc.want[i])
			}
		}
	}
}
