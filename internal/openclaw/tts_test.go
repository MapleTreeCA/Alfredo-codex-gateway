package openclaw

import (
	"context"
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"gateway/internal/audio"
	"gateway/internal/config"
)

func TestPCM16ToWAV(t *testing.T) {
	t.Parallel()

	raw := make([]byte, 4)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(int16(1000)))
	binary.LittleEndian.PutUint16(raw[2:4], uint16(0xfc18))

	wav, err := pcm16ToWAV(24000, raw)
	if err != nil {
		t.Fatalf("pcm16ToWAV failed: %v", err)
	}

	parsed, err := audio.ParseWAV(wav)
	if err != nil {
		t.Fatalf("ParseWAV failed: %v", err)
	}
	if parsed.SampleRate != 24000 {
		t.Fatalf("unexpected sample rate: %d", parsed.SampleRate)
	}
	if len(parsed.Samples) != 2 || parsed.Samples[0] != 1000 || parsed.Samples[1] != -1000 {
		t.Fatalf("unexpected samples: %+v", parsed.Samples)
	}
}

func TestSpeechSynthesizerSynthesizeIntegration(t *testing.T) {
	if os.Getenv("OPENCLAW_TTS_INTEGRATION") != "1" {
		t.Skip("set OPENCLAW_TTS_INTEGRATION=1 to run OpenClaw TTS integration")
	}

	root := os.Getenv("GATEWAY_OPENCLAW_ROOT")
	if root == "" {
		root = "/opt/homebrew/lib/node_modules/openclaw"
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("openclaw root unavailable: %v", err)
	}

	t.Setenv("GATEWAY_OPENCLAW_ROOT", root)
	synth := NewSpeechSynthesizer(config.Config{})
	audio, err := synth.Synthesize(context.Background(), "codex gateway ok")
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}
	if len(audio) == 0 {
		t.Fatal("Synthesize returned empty audio")
	}
}

func TestOpenClawTTSScriptUsesCorrectWordBoundaryEscapes(t *testing.T) {
	t.Parallel()

	if strings.Contains(openClawTTSScript, `new RegExp("\\\\b" + name + "\\\\s+as\\\\s+`) {
		t.Fatal("openClawTTSScript uses over-escaped export alias regex")
	}
	if strings.Contains(openClawTTSScript, `new RegExp("export\\\\s*\\\\{[\\\\s\\\\S]*?\\\\b" + name + "\\\\b`) {
		t.Fatal("openClawTTSScript uses over-escaped direct export regex")
	}
	if !strings.Contains(openClawTTSScript, `new RegExp("\\b" + name + "\\s+as\\s+([A-Za-z_$][A-Za-z0-9_$]*)\\b")`) {
		t.Fatal("openClawTTSScript missing export alias regex")
	}
	if !strings.Contains(openClawTTSScript, `new RegExp("export\\s*\\{[\\s\\S]*?\\b" + name + "\\b[\\s\\S]*?\\}")`) {
		t.Fatal("openClawTTSScript missing direct export regex")
	}
}
