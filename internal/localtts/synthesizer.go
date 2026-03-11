package localtts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gateway/internal/config"
)

type Synthesizer struct {
	voice      string
	rate       int
	sampleRate int
}

type Options struct {
	Voice string
	Rate  int
}

func New(cfg config.Config) *Synthesizer {
	return &Synthesizer{
		voice:      strings.TrimSpace(cfg.LocalTTSVoice),
		rate:       cfg.LocalTTSRate,
		sampleRate: cfg.LocalTTSSampleRate,
	}
}

func (s *Synthesizer) Synthesize(ctx context.Context, text string) ([]byte, error) {
	return s.SynthesizeWithOptions(ctx, text, Options{})
}

func (s *Synthesizer) SynthesizeWithOptions(ctx context.Context, text string, options Options) ([]byte, error) {
	content := strings.TrimSpace(text)
	if content == "" {
		return nil, errors.New("tts input is empty")
	}
	if s.sampleRate <= 0 {
		return nil, errors.New("local tts sample rate must be > 0")
	}
	if _, err := exec.LookPath("say"); err != nil {
		return nil, fmt.Errorf("local tts requires macOS 'say' command: %w", err)
	}
	rate := s.rate
	if options.Rate > 0 {
		rate = options.Rate
	}
	if rate <= 0 {
		return nil, errors.New("local tts rate must be > 0")
	}
	voice := s.voice
	if strings.TrimSpace(options.Voice) != "" {
		voice = strings.TrimSpace(options.Voice)
	}

	tmpDir, err := os.MkdirTemp("", "gateway-local-tts-*")
	if err != nil {
		return nil, fmt.Errorf("create tts temp dir failed: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	aiffPath := filepath.Join(tmpDir, "speech.aiff")
	wavPath := filepath.Join(tmpDir, "speech.wav")

	if err := runSay(ctx, content, voice, aiffPath, rate); err != nil {
		return nil, err
	}
	if err := convertAiffToWav(ctx, aiffPath, wavPath, s.sampleRate); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(wavPath)
	if err != nil {
		return nil, fmt.Errorf("read local tts wav failed: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("local tts produced empty wav")
	}
	return out, nil
}

func runSay(ctx context.Context, text, voice, outPath string, rate int) error {
	args := []string{"-o", outPath, "-r", strconv.Itoa(rate)}
	if strings.TrimSpace(voice) != "" {
		args = append(args, "-v", strings.TrimSpace(voice))
	}
	args = append(args, text)
	cmd := exec.CommandContext(ctx, "say", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("run say failed: %s", compactExecError(err, output))
	}
	return nil
}

func convertAiffToWav(ctx context.Context, inPath, outPath string, sampleRate int) error {
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		args := []string{
			"-v", "error",
			"-y",
			"-i", inPath,
			"-ac", "1",
			"-ar", strconv.Itoa(sampleRate),
			"-f", "wav",
			outPath,
		}
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		if output, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			return fmt.Errorf("convert with ffmpeg failed: %s", compactExecError(err, output))
		}
	}

	if _, err := exec.LookPath("afconvert"); err == nil {
		args := []string{
			"-f", "WAVE",
			"-d", fmt.Sprintf("LEI16@%d", sampleRate),
			inPath,
			outPath,
		}
		cmd := exec.CommandContext(ctx, "afconvert", args...)
		if output, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			return fmt.Errorf("convert with afconvert failed: %s", compactExecError(err, output))
		}
	}

	return errors.New("local tts conversion requires 'ffmpeg' or 'afconvert'")
}

func compactExecError(err error, output []byte) string {
	msg := strings.TrimSpace(string(output))
	if msg == "" {
		msg = err.Error()
	}
	if len(msg) > 500 {
		return msg[:500] + "..."
	}
	return msg
}

func CheckDependencies() error {
	if _, err := exec.LookPath("say"); err != nil {
		return fmt.Errorf("missing dependency: say: %w", err)
	}
	if _, ffmpegErr := exec.LookPath("ffmpeg"); ffmpegErr == nil {
		return nil
	}
	if _, afErr := exec.LookPath("afconvert"); afErr == nil {
		return nil
	}
	return errors.New("missing dependency: need ffmpeg or afconvert")
}
