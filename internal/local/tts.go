package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gateway/internal/config"
)

type SpeechSynthesizer struct {
	baseURL string
	client  *http.Client
}

func NewSpeechSynthesizer(cfg config.Config) *SpeechSynthesizer {
	return &SpeechSynthesizer{
		baseURL: "http://" + strings.TrimSpace(cfg.LocalTTSAddr),
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
	content := strings.TrimSpace(text)
	if content == "" {
		return nil, errors.New("tts input is empty")
	}

	payload := map[string]any{"text": content}
	if options != nil {
		if voice := strings.TrimSpace(options["voice"]); voice != "" {
			payload["voice"] = voice
		}
		if rawRate := strings.TrimSpace(options["rate"]); rawRate != "" {
			if rate, err := strconv.Atoi(rawRate); err == nil && rate > 0 {
				payload["rate"] = rate
			}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal local tts request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		s.baseURL+"/synthesize",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("build local tts request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local tts request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read local tts response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("local tts error (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if len(respBody) == 0 {
		return nil, errors.New("local tts returned empty wav")
	}
	return respBody, nil
}
