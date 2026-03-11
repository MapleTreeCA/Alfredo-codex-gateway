package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

	body, err := json.Marshal(map[string]any{
		"model":           s.model,
		"voice":           voice,
		"input":           text,
		"response_format": "wav",
	})
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

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read tts response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseAPIError("tts", resp.StatusCode, payload)
	}
	return payload, nil
}
