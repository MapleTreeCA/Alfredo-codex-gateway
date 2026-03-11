package local

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

type Transcriber struct {
	baseURL string
	client  *http.Client
}

func NewTranscriber(cfg config.Config) *Transcriber {
	return &Transcriber{
		baseURL: "http://" + strings.TrimSpace(cfg.LocalSTTAddr),
		client: &http.Client{
			Timeout: cfg.STTTimeout + 5*time.Second,
		},
	}
}

func (t *Transcriber) Transcribe(ctx context.Context, wav []byte) (string, error) {
	if len(wav) == 0 {
		return "", errors.New("stt audio payload is empty")
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		t.baseURL+"/transcribe",
		bytes.NewReader(wav),
	)
	if err != nil {
		return "", fmt.Errorf("build local stt request failed: %w", err)
	}
	req.Header.Set("Content-Type", "audio/wav")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("local stt request failed: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read local stt response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("local stt error (%d): %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", fmt.Errorf("parse local stt response failed: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		return "", errors.New("local stt returned empty text")
	}
	return text, nil
}
