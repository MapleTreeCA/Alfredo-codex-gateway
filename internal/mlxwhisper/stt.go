package mlxwhisper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gateway/internal/config"
)

type Transcriber struct {
	bin             string
	model           string
	language        string
	residentEnabled bool
	residentTimeout time.Duration

	mu               sync.Mutex
	worker           *residentWorker
	residentDisabled bool
}

func NewTranscriber(cfg config.Config) *Transcriber {
	return &Transcriber{
		bin:             strings.TrimSpace(cfg.MLXWhisperBin),
		model:           strings.TrimSpace(cfg.MLXWhisperModel),
		language:        normalizeLanguage(cfg.STTLanguage),
		residentEnabled: cfg.MLXWhisperResidentEnabled,
		residentTimeout: cfg.MLXWhisperResidentTimeout,
	}
}

func (t *Transcriber) Transcribe(ctx context.Context, wav []byte) (string, error) {
	if len(wav) == 0 {
		return "", errors.New("stt audio payload is empty")
	}
	if t.residentEnabled {
		text, err := t.transcribeWithResident(ctx, wav)
		if err == nil {
			return text, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		log.Printf("mlx-whisper resident mode unavailable, fallback to one-shot cli: %v", err)
	}
	return t.transcribeViaCLI(ctx, wav)
}

func (t *Transcriber) Close() error {
	t.mu.Lock()
	worker := t.worker
	t.worker = nil
	t.mu.Unlock()

	if worker != nil {
		return worker.Close()
	}
	return nil
}

func (t *Transcriber) transcribeWithResident(ctx context.Context, wav []byte) (string, error) {
	worker, err := t.getOrStartWorker()
	if err != nil {
		return "", err
	}
	text, err := worker.Transcribe(ctx, wav, t.language)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		_ = worker.Close()
		t.mu.Lock()
		if t.worker == worker {
			t.worker = nil
		}
		t.mu.Unlock()
		return "", err
	}
	return text, nil
}

func (t *Transcriber) getOrStartWorker() (*residentWorker, error) {
	t.mu.Lock()
	if t.worker != nil {
		worker := t.worker
		t.mu.Unlock()
		return worker, nil
	}
	if t.residentDisabled {
		t.mu.Unlock()
		return nil, errors.New("resident worker is disabled after startup failure")
	}
	t.mu.Unlock()

	worker, err := newResidentWorker(residentWorkerOptions{
		MLXWhisperBin: t.binary(),
		Model:         t.modelName(),
		ReadyTimeout:  t.residentTimeout,
	})
	if err != nil {
		t.mu.Lock()
		t.residentDisabled = true
		t.mu.Unlock()
		return nil, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.worker != nil {
		_ = worker.Close()
		return t.worker, nil
	}
	t.worker = worker
	return worker, nil
}

func (t *Transcriber) binary() string {
	bin := strings.TrimSpace(t.bin)
	if bin == "" {
		bin = "mlx_whisper"
	}
	return bin
}

func (t *Transcriber) modelName() string {
	model := strings.TrimSpace(t.model)
	if model == "" {
		model = "mlx-community/whisper-large-v3-turbo"
	}
	return model
}

func (t *Transcriber) transcribeViaCLI(ctx context.Context, wav []byte) (string, error) {
	bin := t.binary()
	if _, err := exec.LookPath(bin); err != nil {
		return "", fmt.Errorf("mlx-whisper binary not found: %w", err)
	}

	model := t.modelName()

	audioFile, err := os.CreateTemp("", "gateway-mlx-whisper-*.wav")
	if err != nil {
		return "", fmt.Errorf("create temp audio file failed: %w", err)
	}
	audioPath := audioFile.Name()
	defer os.Remove(audioPath)

	if _, err := audioFile.Write(wav); err != nil {
		_ = audioFile.Close()
		return "", fmt.Errorf("write temp audio file failed: %w", err)
	}
	if err := audioFile.Close(); err != nil {
		return "", fmt.Errorf("close temp audio file failed: %w", err)
	}

	outputDir, err := os.MkdirTemp("", "gateway-mlx-whisper-out-*")
	if err != nil {
		return "", fmt.Errorf("create temp output dir failed: %w", err)
	}
	defer os.RemoveAll(outputDir)

	const outputName = "transcript"

	args := []string{
		"--model", model,
		"--task", "transcribe",
		"--output-format", "json",
		"--output-name", outputName,
		"--output-dir", outputDir,
	}
	if t.language != "" {
		args = append(args, "--language", t.language)
	}
	args = append(args, audioPath)

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "PYTORCH_ENABLE_MPS_FALLBACK=1")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errText := strings.TrimSpace(stderr.String())
		if errText == "" {
			errText = strings.TrimSpace(stdout.String())
		}
		if errText == "" {
			errText = err.Error()
		}
		return "", fmt.Errorf("mlx-whisper command failed: %s", errText)
	}

	payload, err := os.ReadFile(filepath.Join(outputDir, outputName+".json"))
	if err != nil {
		return "", fmt.Errorf("read mlx-whisper output failed: %w", err)
	}

	var resp struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return "", fmt.Errorf("parse mlx-whisper output failed: %w", err)
	}

	text := strings.TrimSpace(resp.Text)
	if text == "" {
		return "", errors.New("mlx-whisper returned empty text")
	}
	return text, nil
}

func normalizeLanguage(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || s == "auto" {
		return ""
	}
	switch s {
	case "zh-cn", "zh-sg", "zh-tw", "zh-hk":
		return "zh"
	case "en-us", "en-gb", "en-au", "en-ca":
		return "en"
	case "ja-jp":
		return "ja"
	case "ko-kr":
		return "ko"
	}
	if idx := strings.IndexByte(s, '-'); idx > 0 {
		return s[:idx]
	}
	return s
}
