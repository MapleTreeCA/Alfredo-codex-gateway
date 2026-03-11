package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"gateway/internal/config"
)

type Transcriber struct {
	apiKey   string
	baseURL  string
	model    string
	language string
	client   *http.Client
}

func NewTranscriber(cfg config.Config) *Transcriber {
	return &Transcriber{
		apiKey:   strings.TrimSpace(cfg.OpenAIAPIKey),
		baseURL:  strings.TrimRight(strings.TrimSpace(cfg.OpenAIBaseURL), "/"),
		model:    strings.TrimSpace(cfg.STTModel),
		language: strings.TrimSpace(cfg.STTLanguage),
		client: &http.Client{
			Timeout: cfg.STTTimeout + 5*time.Second,
		},
	}
}

func (t *Transcriber) Transcribe(ctx context.Context, wav []byte) (string, error) {
	if t.apiKey == "" {
		return "", errors.New("OPENAI_API_KEY is empty")
	}
	if len(wav) == 0 {
		return "", errors.New("stt audio payload is empty")
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	filePart, err := writer.CreateFormFile("file", "speech.wav")
	if err != nil {
		return "", fmt.Errorf("create stt form file failed: %w", err)
	}
	if _, err := filePart.Write(wav); err != nil {
		return "", fmt.Errorf("write stt wav failed: %w", err)
	}
	if err := writer.WriteField("model", t.model); err != nil {
		return "", fmt.Errorf("write stt model failed: %w", err)
	}
	if lang := normalizeLanguage(t.language); lang != "" {
		if err := writer.WriteField("language", lang); err != nil {
			return "", fmt.Errorf("write stt language failed: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close stt body failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/audio/transcriptions", body)
	if err != nil {
		return "", fmt.Errorf("build stt request failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("stt request failed: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read stt response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", parseAPIError("stt", resp.StatusCode, payload)
	}

	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", fmt.Errorf("parse stt response failed: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		return "", errors.New("stt returned empty text")
	}
	return text, nil
}

func normalizeLanguage(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" || value == "auto" {
		return ""
	}
	if idx := strings.IndexByte(value, '-'); idx > 0 {
		return value[:idx]
	}
	return value
}

func parseAPIError(scope string, statusCode int, payload []byte) error {
	var out struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &out); err == nil && strings.TrimSpace(out.Error.Message) != "" {
		return fmt.Errorf("%s api error (%d): %s", scope, statusCode, strings.TrimSpace(out.Error.Message))
	}
	return fmt.Errorf("%s api error (%d): %s", scope, statusCode, strings.TrimSpace(string(payload)))
}
