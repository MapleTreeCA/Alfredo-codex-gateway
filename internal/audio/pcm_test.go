package audio

import "testing"

func TestWAVRoundTrip(t *testing.T) {
	original := []int16{0, 1200, -1200, 3200, -3200}
	wav, err := EncodeWAV(16000, original)
	if err != nil {
		t.Fatalf("EncodeWAV failed: %v", err)
	}

	parsed, err := ParseWAV(wav)
	if err != nil {
		t.Fatalf("ParseWAV failed: %v", err)
	}

	if parsed.SampleRate != 16000 {
		t.Fatalf("unexpected sample rate %d", parsed.SampleRate)
	}
	if parsed.Channels != 1 {
		t.Fatalf("unexpected channel count %d", parsed.Channels)
	}
	if len(parsed.Samples) != len(original) {
		t.Fatalf("unexpected sample count %d", len(parsed.Samples))
	}
	for i := range original {
		if parsed.Samples[i] != original[i] {
			t.Fatalf("sample %d mismatch: got %d want %d", i, parsed.Samples[i], original[i])
		}
	}
}

func TestMixToMono(t *testing.T) {
	stereo := PCM{
		SampleRate: 24000,
		Channels:   2,
		Samples:    []int16{100, 300, -200, 200},
	}

	mono := MixToMono(stereo)
	if mono.Channels != 1 {
		t.Fatalf("unexpected channel count %d", mono.Channels)
	}
	if len(mono.Samples) != 2 {
		t.Fatalf("unexpected mono sample count %d", len(mono.Samples))
	}
	if mono.Samples[0] != 200 || mono.Samples[1] != 0 {
		t.Fatalf("unexpected mono samples %v", mono.Samples)
	}
}
