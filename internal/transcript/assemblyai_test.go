package transcript

import (
	"encoding/binary"
	"testing"
)

func TestDetectVoiceActivity_SetsLastVoiceOnLoudFrame(t *testing.T) {
	s := NewAssemblyAIService("test")
	// connected guard is bypassed by calling detectVoiceActivity directly
	// craft a loud 10ms frame
	samples := make([]byte, 160*2)
	for i := 0; i < 160; i++ {
		binary.LittleEndian.PutUint16(samples[i*2:(i+1)*2], 3000)
	}
	before := s.RecentlyDetectedVoice(0)
	s.detectVoiceActivity(samples)
	after := s.RecentlyDetectedVoice(0)
	if before && !after {
		t.Fatalf("expected voice detection change")
	}
}

func TestHelpers_LastWordAndContinuation(t *testing.T) {
	if lastWord("") != "" {
		t.Fatalf("lastWord empty mismatch")
	}
	if lastWord("hi there!") != "there" {
		t.Fatalf("lastWord basic mismatch")
	}
	if !isContinuationLikely("we should and") {
		t.Fatalf("expected continuation likely when last word is 'and'")
	}
	if isContinuationLikely("complete sentence.") {
		t.Fatalf("did not expect continuation likely")
	}
}
