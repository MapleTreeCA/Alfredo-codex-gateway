package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gateway/internal/config"
)

type SpeechSynthesizer struct {
	apiKey  string
	baseURL string
	model   string
	voice   string
	client  *http.Client
}

func NewSpeechSynthesizer(cfg config.Config) *SpeechSynthesizer {
	return &SpeechSynthesizer{
		apiKey:  strings.TrimSpace(cfg.OpenAIAPIKey),
		baseURL: strings.TrimRight(strings.TrimSpace(cfg.OpenAIBaseURL), "/"),
		model:   strings.TrimSpace(cfg.TTSModel),
		voice:   strings.TrimSpace(cfg.TTSVoice),
		client: &http.Client{
			Timeout: cfg.TTSTimeout + 5*time.Second,
		},
	}
}

func (s *SpeechSynthesizer) Synthesize(ctx context.Context, text string) ([]byte, error) {
	return s.SynthesizeWithOptions(ctx, text, nil)
}

func (s *SpeechSynthesizer) SynthesizeWithOptions(
	ctx context.Context,
	text string,
	options map[string]string,
) ([]byte, error) {
	if s.apiKey == "" {
		return nil, errors.New("OPENAI_API_KEY is empty")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("tts input is empty")
	}
	voice := s.voice
	if options != nil {
		if override := strings.TrimSpace(options["voice"]); override != "" {
			voice = override
		}
	}

	payload := map[string]any{
		"model":           s.model,
		"voice":           voice,
		"input":           text,
		"response_format": "wav",
	}
	if options != nil {
		if speed, ok := parseOpenAITTSSpeed(options["rate"]); ok {
			payload["speed"] = speed
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal tts request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build tts request failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tts request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read tts response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseAPIError("tts", resp.StatusCode, respBody)
	}
	return respBody, nil
}

func parseOpenAITTSSpeed(raw string) (float64, bool) {
	rate, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || rate <= 0 {
		return 0, false
	}

	speed := float64(rate) / 220.0
	if speed < 0.25 {
		speed = 0.25
	}
	if speed > 4.0 {
		speed = 4.0
	}
	return math.Round(speed*100) / 100, true
}
