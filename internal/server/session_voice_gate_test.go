package server

import (
	"errors"
	"math"
	"testing"

	"gateway/internal/audio"
	"gateway/internal/config"
)

func TestDetachTurnDropsWeakNoiseTurn(t *testing.T) {
	sampleRate := 16000
	frameMS := 60
	frameCount := 34
	pcm := make([]int16, sampleRate*frameMS/1000*frameCount)
	frames, err := audio.EncodeOpusFrames(sampleRate, frameMS, pcm)
	if err != nil {
		t.Fatalf("encode opus failed: %v", err)
	}

	s := &Session{
		id: "test-noise",
		currentTurn: &turnBuffer{
			id:               1,
			sampleRate:       sampleRate,
			frameDurationMS:  frameMS,
			frames:           frames,
			audioBytes:       totalFrameBytes(frames),
			speechDetected:   true, // simulate weak VAD false-positive
			speechFrameCount: 2,
			maxFramePeak:     160,
			maxFrameAvg:      16,
		},
	}

	turn, _, err := s.detachTurn("silence_timeout")
	if !errors.Is(err, errNoTurn) {
		t.Fatalf("expected errNoTurn, got: %v", err)
	}
	if turn != nil {
		t.Fatalf("expected dropped turn, got non-nil")
	}
}

func TestDetachTurnKeepsSpeechTurn(t *testing.T) {
	sampleRate := 16000
	frameMS := 60
	frameCount := 34
	frameSize := sampleRate * frameMS / 1000
	pcm := make([]int16, frameSize*frameCount)
	for i := range pcm {
		phase := 2.0 * math.Pi * 440.0 * float64(i) / float64(sampleRate)
		pcm[i] = int16(math.Sin(phase) * 2000.0)
	}
	frames, err := audio.EncodeOpusFrames(sampleRate, frameMS, pcm)
	if err != nil {
		t.Fatalf("encode opus failed: %v", err)
	}

	s := &Session{
		id: "test-speech",
		currentTurn: &turnBuffer{
			id:               2,
			sampleRate:       sampleRate,
			frameDurationMS:  frameMS,
			frames:           frames,
			audioBytes:       totalFrameBytes(frames),
			speechDetected:   true,
			speechFrameCount: 8,
			maxFramePeak:     2200,
			maxFrameAvg:      520,
		},
	}

	turn, _, err := s.detachTurn("silence_timeout")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if turn == nil {
		t.Fatalf("expected non-nil turn")
	}
}

func TestDetachTurnDropsMaxTimeoutWithoutInterimEvidence(t *testing.T) {
	sampleRate := 16000
	frameMS := 60
	frameCount := 198
	frameSize := sampleRate * frameMS / 1000
	pcm := make([]int16, frameSize*frameCount)
	for i := range pcm {
		phase := 2.0 * math.Pi * 220.0 * float64(i) / float64(sampleRate)
		pcm[i] = int16(math.Sin(phase) * 1800.0)
	}
	frames, err := audio.EncodeOpusFrames(sampleRate, frameMS, pcm)
	if err != nil {
		t.Fatalf("encode opus failed: %v", err)
	}

	s := &Session{
		id: "test-max-timeout-drop",
		server: &Server{
			cfg: config.Config{
				STTStreamingEnabled: true,
			},
		},
		currentTurn: &turnBuffer{
			id:               3,
			sampleRate:       sampleRate,
			frameDurationMS:  frameMS,
			frames:           frames,
			audioBytes:       totalFrameBytes(frames),
			speechDetected:   true,
			speechFrameCount: 123,
			maxFramePeak:     7840,
			maxFrameAvg:      331,
			interimUpdates:   1,
		},
	}

	turn, _, err := s.detachTurn("max_turn_timeout")
	if !errors.Is(err, errNoTurn) {
		t.Fatalf("expected errNoTurn, got: %v", err)
	}
	if turn != nil {
		t.Fatalf("expected dropped turn, got non-nil")
	}
}

func TestDetachTurnKeepsMaxTimeoutWithInterimEvidence(t *testing.T) {
	sampleRate := 16000
	frameMS := 60
	frameCount := 198
	frameSize := sampleRate * frameMS / 1000
	pcm := make([]int16, frameSize*frameCount)
	for i := range pcm {
		phase := 2.0 * math.Pi * 220.0 * float64(i) / float64(sampleRate)
		pcm[i] = int16(math.Sin(phase) * 1800.0)
	}
	frames, err := audio.EncodeOpusFrames(sampleRate, frameMS, pcm)
	if err != nil {
		t.Fatalf("encode opus failed: %v", err)
	}

	s := &Session{
		id: "test-max-timeout-keep",
		server: &Server{
			cfg: config.Config{
				STTStreamingEnabled: true,
			},
		},
		currentTurn: &turnBuffer{
			id:               4,
			sampleRate:       sampleRate,
			frameDurationMS:  frameMS,
			frames:           frames,
			audioBytes:       totalFrameBytes(frames),
			speechDetected:   true,
			speechFrameCount: 123,
			maxFramePeak:     7840,
			maxFrameAvg:      331,
			interimUpdates:   3,
		},
	}

	turn, _, err := s.detachTurn("max_turn_timeout")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if turn == nil {
		t.Fatalf("expected non-nil turn")
	}
}

func totalFrameBytes(frames [][]byte) int {
	total := 0
	for _, frame := range frames {
		total += len(frame)
	}
	return total
}
