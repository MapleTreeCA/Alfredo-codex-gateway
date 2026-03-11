package server

import (
	"testing"
	"time"
)

func TestMinimumInterimFrames(t *testing.T) {
	if got := minimumInterimFrames(1200*time.Millisecond, 60); got != 20 {
		t.Fatalf("frames mismatch: got=%d want=%d", got, 20)
	}
	if got := minimumInterimFrames(1*time.Millisecond, 60); got != 1 {
		t.Fatalf("frames mismatch: got=%d want=%d", got, 1)
	}
	if got := minimumInterimFrames(1200*time.Millisecond, 0); got != 1 {
		t.Fatalf("frames mismatch: got=%d want=%d", got, 1)
	}
}

func TestShouldTriggerInterimSTT(t *testing.T) {
	now := time.Now()
	interval := 900 * time.Millisecond
	minAudio := 1200 * time.Millisecond

	turn := &turnBuffer{
		mode:            "realtime",
		frameDurationMS: 60,
		frames:          make([][]byte, 20),
	}
	if !shouldTriggerInterimSTT(now, turn, interval, minAudio) {
		t.Fatalf("expected interim trigger for valid turn")
	}

	turn.mode = "manual"
	if !shouldTriggerInterimSTT(now, turn, interval, minAudio) {
		t.Fatalf("expected interim trigger for manual mode as well")
	}

	turn.mode = "auto"
	turn.frames = make([][]byte, 10)
	if shouldTriggerInterimSTT(now, turn, interval, minAudio) {
		t.Fatalf("did not expect trigger for insufficient audio")
	}

	turn.frames = make([][]byte, 20)
	turn.interimInFlight = true
	if shouldTriggerInterimSTT(now, turn, interval, minAudio) {
		t.Fatalf("did not expect trigger while in-flight")
	}

	turn.interimInFlight = false
	turn.interimLastAt = now.Add(-300 * time.Millisecond)
	if shouldTriggerInterimSTT(now, turn, interval, minAudio) {
		t.Fatalf("did not expect trigger before interval elapsed")
	}

	turn.interimLastAt = now.Add(-2 * time.Second)
	turn.interimFrameLen = len(turn.frames) - 1
	if shouldTriggerInterimSTT(now, turn, interval, minAudio) {
		t.Fatalf("did not expect trigger with only one new frame")
	}

	turn.interimFrameLen = len(turn.frames) - 2
	if !shouldTriggerInterimSTT(now, turn, interval, minAudio) {
		t.Fatalf("expected trigger with two new frames")
	}
}
