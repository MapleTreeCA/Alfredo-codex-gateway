package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gateway/internal/config"
)

func TestSpeechSynthesizerSendsSpeedFromRateOption(t *testing.T) {
	t.Parallel()

	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/speech" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body failed: %v", err)
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal request body failed: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("RIFFfakewav"))
	}))
	defer server.Close()

	synth := NewSpeechSynthesizer(config.Config{
		OpenAIAPIKey:  "test-key",
		OpenAIBaseURL: server.URL,
		TTSModel:      "tts-1",
		TTSVoice:      "alloy",
	})

	wav, err := synth.SynthesizeWithOptions(context.Background(), "hello", map[string]string{
		"voice": "nova",
		"rate":  "330",
	})
	if err != nil {
		t.Fatalf("SynthesizeWithOptions failed: %v", err)
	}
	if string(wav) != "RIFFfakewav" {
		t.Fatalf("unexpected response body: %q", string(wav))
	}
	if got["voice"] != "nova" {
		t.Fatalf("expected voice override, got %#v", got["voice"])
	}
	speed, ok := got["speed"].(float64)
	if !ok {
		t.Fatalf("expected speed float64, got %#v", got["speed"])
	}
	if speed != 1.5 {
		t.Fatalf("expected speed 1.5, got %v", speed)
	}
}

func TestParseOpenAITTSSpeed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want float64
		ok   bool
	}{
		{name: "default_rate", raw: "220", want: 1.0, ok: true},
		{name: "faster", raw: "330", want: 1.5, ok: true},
		{name: "lower_bound", raw: "40", want: 0.25, ok: true},
		{name: "upper_bound", raw: "1000", want: 4.0, ok: true},
		{name: "invalid", raw: "fast", want: 0, ok: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseOpenAITTSSpeed(tc.raw)
			if ok != tc.ok {
				t.Fatalf("expected ok=%t, got %t", tc.ok, ok)
			}
			if got != tc.want {
				t.Fatalf("expected speed %v, got %v", tc.want, got)
			}
		})
	}
}
