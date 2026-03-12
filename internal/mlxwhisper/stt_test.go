package mlxwhisper

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gateway/internal/config"
)

func TestTranscribeReadsJSONOutput(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "mlx_whisper")
	script := `#!/bin/sh
set -eu
outdir=""
outname=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-dir)
      outdir="$2"
      shift 2
      ;;
    --output-name)
      outname="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
printf '{"text":"transcription success"}' > "$outdir/$outname.json"
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake mlx_whisper: %v", err)
	}

	transcriber := NewTranscriber(config.Config{
		MLXWhisperBin:             binPath,
		MLXWhisperModel:           "mlx-community/whisper-large-v3-turbo",
		MLXWhisperResidentEnabled: true,
		MLXWhisperResidentTimeout: 300 * time.Millisecond,
		STTLanguage:               "zh-CN",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	text, err := transcriber.Transcribe(ctx, []byte("RIFFfakewav"))
	if err != nil {
		t.Fatalf("Transcribe returned error: %v", err)
	}
	if text != "transcription success" {
		t.Fatalf("unexpected transcript %q", text)
	}
}
