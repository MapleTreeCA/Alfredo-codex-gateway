package codexauth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gateway/internal/config"
	"gateway/internal/memorystore"
)

const (
	tokenURL                = "https://auth.openai.com/oauth/token"
	clientID                = "app_EMoamEEZ73f0CkXaXp7hrann"
	jwtClaims               = "https://api.openai.com/auth"
	defaultReasoningLevel   = "medium"
	defaultTextVerbosity    = "medium"
	defaultHistoryLimit     = 10
	defaultRecallDays       = 30
	defaultRecallMaxItems   = 3
	defaultRecallItemMax    = 110
	defaultRecallTotalMax   = 280
	defaultRecallCandidates = 24
	minRecallQueryRunes     = 3
	defaultMaxOutputTokens  = 500
	conciseInstructionRule  = "Be concise."
)

type Client struct {
	authFile     string
	baseURL      string
	model        string
	instructions string
	maxOutput    int
	httpClient   *http.Client
	memoryStore  *memorystore.Store
	maxHistory   int
	mu           sync.Mutex
	historyMu    sync.Mutex
	history      map[string][]conversationMessage
}

type authState struct {
	Type    string `json:"type"`
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type sseEvent struct {
	Type     string         `json:"type"`
	Delta    string         `json:"delta"`
	Text     string         `json:"text"`
	Response map[string]any `json:"response"`
}

type generateOptions struct {
	Model            string
	Effort           string
	Verbosity        string
	Online           bool
	Concise          bool
	HasConcise       bool
	MaxOutputTokens  int
	ContextMessages  int
	MemoryRecallDays int
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type GenerateDetailedResponse struct {
	Reply          string `json:"reply"`
	Usage          Usage  `json:"usage"`
	MemoryMessages int    `json:"memory_messages"`
	SentMessages   int    `json:"sent_messages"`
}

type generateResult struct {
	Reply          string
	Usage          Usage
	MemoryMessages int
	SentMessages   int
}

type conversationMessage struct {
	Role string
	Text string
}

type memoryHintStats struct {
	Candidates           int
	InjectedCount        int
	InjectedRawRunes     int
	InjectedSnippetRunes int
	InjectedHintRunes    int
	SourceSessions       []string
}

type memoryHintBuildResult struct {
	Hints string
	Stats memoryHintStats
}

func New(cfg config.Config) *Client {
	return NewWithMemoryStore(cfg, nil)
}

func NewWithMemoryStore(cfg config.Config, store *memorystore.Store) *Client {
	historyLimit := cfg.MemoryContextSize
	if historyLimit <= 0 {
		historyLimit = defaultHistoryLimit
	}
	return &Client{
		authFile:     cfg.CodexAuthFile,
		baseURL:      strings.TrimRight(cfg.CodexBaseURL, "/"),
		model:        normalizeModel(cfg.CodexModel),
		instructions: strings.TrimSpace(cfg.CodexInstructions),
		maxOutput:    resolveMaxOutputTokens(cfg.CodexMaxOutputTokens),
		memoryStore:  store,
		maxHistory:   historyLimit,
		history:      make(map[string][]conversationMessage),
		httpClient: &http.Client{
			Timeout: cfg.CodexTimeout,
		},
	}
}

func (c *Client) Generate(ctx context.Context, sessionID, userText string) (string, error) {
	result, err := c.generate(ctx, sessionID, userText, generateOptions{})
	if err != nil {
		return "", err
	}
	return result.Reply, nil
}

func (c *Client) GenerateWithOptions(
	ctx context.Context,
	sessionID, userText string,
	options map[string]string,
) (string, error) {
	concise, hasConcise := parseOptionalBool(options["concise"])
	result, err := c.generate(ctx, sessionID, userText, generateOptions{
		Model:            strings.TrimSpace(options["model"]),
		Effort:           strings.TrimSpace(options["effort"]),
		Verbosity:        strings.TrimSpace(options["verbosity"]),
		Online:           normalizeOnlineFlag(options["online"]),
		Concise:          concise,
		HasConcise:       hasConcise,
		MaxOutputTokens:  parseMaxOutputTokens(options["max_output_tokens"]),
		ContextMessages:  parseContextMessages(options["context_messages"]),
		MemoryRecallDays: parseMemoryRecallDays(options["memory_recall_days"]),
	})
	if err != nil {
		return "", err
	}
	return result.Reply, nil
}

func (c *Client) GenerateWithOptionsDetailed(
	ctx context.Context,
	sessionID, userText string,
	options map[string]string,
) (GenerateDetailedResponse, error) {
	concise, hasConcise := parseOptionalBool(options["concise"])
	result, err := c.generate(ctx, sessionID, userText, generateOptions{
		Model:            strings.TrimSpace(options["model"]),
		Effort:           strings.TrimSpace(options["effort"]),
		Verbosity:        strings.TrimSpace(options["verbosity"]),
		Online:           normalizeOnlineFlag(options["online"]),
		Concise:          concise,
		HasConcise:       hasConcise,
		MaxOutputTokens:  parseMaxOutputTokens(options["max_output_tokens"]),
		ContextMessages:  parseContextMessages(options["context_messages"]),
		MemoryRecallDays: parseMemoryRecallDays(options["memory_recall_days"]),
	})
	if err != nil {
		return GenerateDetailedResponse{}, err
	}
	return GenerateDetailedResponse{
		Reply:          result.Reply,
		Usage:          result.Usage,
		MemoryMessages: result.MemoryMessages,
		SentMessages:   result.SentMessages,
	}, nil
}

func (c *Client) generate(
	ctx context.Context,
	sessionID, userText string,
	options generateOptions,
) (generateResult, error) {
	auth, accountID, err := c.ensureAuth(ctx)
	if err != nil {
		return generateResult{}, err
	}

	model := c.model
	if raw := strings.TrimSpace(options.Model); raw != "" {
		model = normalizeModel(raw)
	}
	effort := normalizeReasoningEffort(options.Effort)
	verbosity := normalizeTextVerbosity(options.Verbosity)
	contextLimit := c.resolveContextLimit(options.ContextMessages)
	recallDays := c.resolveRecallDays(options.MemoryRecallDays)
	history := c.loadHistory(sessionID, contextLimit)
	memoryHintResult := c.buildRelevantMemoryHintsDetailed(userText, history, contextLimit, recallDays)
	memoryHints := memoryHintResult.Hints
	if memoryHintResult.Stats.Candidates > 0 || memoryHintResult.Stats.InjectedCount > 0 {
		log.Printf(
			"codex memory recall session=%s recall_days=%d query_runes=%d candidates=%d injected=%d source_sessions=%s raw_runes=%d snippet_runes=%d hint_runes=%d",
			strings.TrimSpace(sessionID),
			recallDays,
			utf8Len(strings.TrimSpace(userText)),
			memoryHintResult.Stats.Candidates,
			memoryHintResult.Stats.InjectedCount,
			strings.Join(memoryHintResult.Stats.SourceSessions, ","),
			memoryHintResult.Stats.InjectedRawRunes,
			memoryHintResult.Stats.InjectedSnippetRunes,
			memoryHintResult.Stats.InjectedHintRunes,
		)
	}
	instructions := composeInstructions(c.instructions, model)
	instructions = composeInstructionsWithMemoryHints(instructions, memoryHints)
	instructions = composeInstructionsWithConciseRuleOption(instructions, options.Concise, options.HasConcise)
	inputMessages := buildInputMessagesFromHistory(history, userText)
	memoryMessages := len(history)

	payload := map[string]any{
		"model":            model,
		"store":            false,
		"stream":           true,
		"instructions":     instructions,
		"prompt_cache_key": sessionID,
		"input":            inputMessages,
		"include":          []string{"reasoning.encrypted_content"},
		"reasoning": map[string]string{
			"effort":  effort,
			"summary": "auto",
		},
		"text": map[string]string{
			"verbosity": verbosity,
		},
	}
	if options.Online {
		payload["tools"] = []map[string]any{
			{
				"type": "web_search",
			},
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return generateResult{}, fmt.Errorf("marshal codex request failed: %w", err)
	}

	doRequest := func(requestBody []byte) (*http.Response, error) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/codex/responses", bytes.NewReader(requestBody))
		if reqErr != nil {
			return nil, fmt.Errorf("build codex request failed: %w", reqErr)
		}
		req.Header.Set("Authorization", "Bearer "+auth.Access)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("chatgpt-account-id", accountID)
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("originator", "codex_cli_rs")
		req.Header.Set("conversation_id", sessionID)
		req.Header.Set("session_id", sessionID)
		resp, reqErr := c.httpClient.Do(req)
		if reqErr != nil {
			return nil, fmt.Errorf("codex request failed: %w", reqErr)
		}
		return resp, nil
	}

	resp, err := doRequest(body)
	if err != nil {
		return generateResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
		return generateResult{}, fmt.Errorf("codex api error (%d): %s", resp.StatusCode, strings.TrimSpace(string(errorBody)))
	}

	streamResult, err := parseSSE(resp.Body)
	if err != nil {
		return generateResult{}, err
	}
	text := strings.TrimSpace(streamResult.Reply)
	if text == "" {
		return generateResult{}, errors.New("codex returned empty text")
	}
	if err := c.appendHistory(sessionID, userText, text); err != nil {
		return generateResult{}, err
	}
	return generateResult{
		Reply:          text,
		Usage:          streamResult.Usage,
		MemoryMessages: memoryMessages,
		SentMessages:   len(inputMessages),
	}, nil
}

func (c *Client) ensureAuth(ctx context.Context) (authState, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	auth, err := c.readAuth()
	if err != nil {
		return authState{}, "", err
	}
	if auth.Type != "oauth" {
		return authState{}, "", errors.New("codex auth file is not an oauth credential")
	}
	if auth.Access == "" || auth.Refresh == "" {
		return authState{}, "", errors.New("codex auth file is missing tokens")
	}
	if auth.Expires <= time.Now().Add(60*time.Second).UnixMilli() {
		refreshed, err := c.refresh(ctx, auth.Refresh)
		if err != nil {
			return authState{}, "", err
		}
		auth = refreshed
		if err := c.writeAuth(auth); err != nil {
			return authState{}, "", err
		}
	}

	accountID, err := extractAccountID(auth.Access)
	if err != nil {
		return authState{}, "", err
	}
	return auth, accountID, nil
}

func (c *Client) readAuth() (authState, error) {
	raw, err := os.ReadFile(c.authFile)
	if err != nil {
		return authState{}, fmt.Errorf("read codex auth file failed: %w", err)
	}
	var auth authState
	if err := json.Unmarshal(raw, &auth); err != nil {
		return authState{}, fmt.Errorf("parse codex auth file failed: %w", err)
	}
	return auth, nil
}

func (c *Client) writeAuth(auth authState) error {
	body, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal refreshed auth failed: %w", err)
	}
	if err := os.WriteFile(c.authFile, body, 0o600); err != nil {
		return fmt.Errorf("write refreshed auth failed: %w", err)
	}
	return nil
}

func (c *Client) refresh(ctx context.Context, refreshToken string) (authState, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return authState{}, fmt.Errorf("build token refresh request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return authState{}, fmt.Errorf("refresh codex token failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return authState{}, fmt.Errorf("read token refresh response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return authState{}, fmt.Errorf("token refresh error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var token tokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return authState{}, fmt.Errorf("parse token refresh response failed: %w", err)
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.ExpiresIn <= 0 {
		return authState{}, errors.New("token refresh response is missing fields")
	}

	return authState{
		Type:    "oauth",
		Access:  token.AccessToken,
		Refresh: token.RefreshToken,
		Expires: time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).UnixMilli(),
	}, nil
}

func parseSSE(reader io.Reader) (generateResult, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var streamed strings.Builder
	var usage Usage
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "[DONE]" {
			continue
		}

		var event sseEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}

		switch event.Type {
		case "response.output_text.delta":
			streamed.WriteString(event.Delta)
		case "response.completed", "response.done":
			usage = mergeUsage(usage, extractUsage(event.Response))
			if text := extractResponseText(event.Response); strings.TrimSpace(text) != "" {
				return generateResult{Reply: text, Usage: usage}, nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return generateResult{}, fmt.Errorf("read codex sse failed: %w", err)
	}
	if streamed.Len() > 0 {
		return generateResult{Reply: streamed.String(), Usage: usage}, nil
	}
	return generateResult{}, errors.New("codex sse stream did not contain a final response")
}

func ExtractAccountID(accessToken string) (string, error) {
	return extractAccountID(accessToken)
}

func extractResponseText(response map[string]any) string {
	var parts []string
	var walk func(any)
	walk = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			typ, _ := value["type"].(string)
			if (typ == "output_text" || typ == "text") && strings.TrimSpace(stringValue(value["text"])) != "" {
				parts = append(parts, strings.TrimSpace(stringValue(value["text"])))
				return
			}
			if child, ok := value["output"]; ok {
				walk(child)
			}
			if child, ok := value["content"]; ok {
				walk(child)
			}
		case []any:
			for _, item := range value {
				walk(item)
			}
		}
	}
	walk(response)
	return strings.Join(parts, "\n")
}

func extractUsage(response map[string]any) Usage {
	if len(response) == 0 {
		return Usage{}
	}

	var found Usage
	var walk func(any)
	walk = func(node any) {
		if found.TotalTokens > 0 || (found.InputTokens > 0 && found.OutputTokens > 0) {
			return
		}
		switch value := node.(type) {
		case map[string]any:
			if usageNode, ok := value["usage"]; ok {
				walk(usageNode)
			}
			inTokens := intValue(value["input_tokens"])
			outTokens := intValue(value["output_tokens"])
			totalTokens := intValue(value["total_tokens"])
			if inTokens > 0 || outTokens > 0 || totalTokens > 0 {
				found = Usage{
					InputTokens:  inTokens,
					OutputTokens: outTokens,
					TotalTokens:  totalTokens,
				}
				return
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, item := range value {
				walk(item)
			}
		}
	}
	walk(response)
	return found
}

func mergeUsage(base, extra Usage) Usage {
	if extra.InputTokens > 0 {
		base.InputTokens = extra.InputTokens
	}
	if extra.OutputTokens > 0 {
		base.OutputTokens = extra.OutputTokens
	}
	if extra.TotalTokens > 0 {
		base.TotalTokens = extra.TotalTokens
	}
	if base.TotalTokens == 0 && (base.InputTokens > 0 || base.OutputTokens > 0) {
		base.TotalTokens = base.InputTokens + base.OutputTokens
	}
	return base
}

func extractAccountID(accessToken string) (string, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return "", errors.New("access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode jwt payload failed: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("parse jwt payload failed: %w", err)
	}
	authClaims, ok := claims[jwtClaims].(map[string]any)
	if !ok {
		return "", errors.New("jwt is missing OpenAI auth claims")
	}
	accountID := stringValue(authClaims["chatgpt_account_id"])
	if accountID == "" {
		return "", errors.New("jwt is missing chatgpt_account_id")
	}
	return accountID, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return 0
}

func normalizeModel(model string) string {
	value := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(value, "gpt-5.2-codex"):
		return "gpt-5.2-codex"
	case strings.Contains(value, "gpt-5.2"):
		return "gpt-5.2"
	case strings.Contains(value, "gpt-5.1-codex-max"):
		return "gpt-5.1-codex-max"
	case strings.Contains(value, "gpt-5.1-codex-mini"), strings.Contains(value, "codex-mini-latest"):
		return "gpt-5.1-codex-mini"
	case strings.Contains(value, "codex"):
		return "gpt-5.1-codex"
	case strings.Contains(value, "gpt-5"):
		return "gpt-5.1"
	default:
		return "gpt-5.1-codex"
	}
}

func normalizeReasoningEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "low":
		return "low"
	case "high":
		return "high"
	case "medium", "minimal", "":
		return defaultReasoningLevel
	default:
		return defaultReasoningLevel
	}
}

func normalizeTextVerbosity(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "low":
		return "low"
	case "high":
		return "high"
	case "medium", "":
		return defaultTextVerbosity
	default:
		return defaultTextVerbosity
	}
}

func normalizeOnlineFlag(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (c *Client) buildInputMessages(sessionID, userText string, historyLimit int) ([]map[string]any, int) {
	history := c.loadHistory(sessionID, historyLimit)
	return buildInputMessagesFromHistory(history, userText), len(history)
}

func buildInputMessagesFromHistory(history []conversationMessage, userText string) []map[string]any {
	user := strings.TrimSpace(userText)
	if user == "" {
		user = userText
	}
	input := make([]map[string]any, 0, len(history)+1)
	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			continue
		}
		contentType := "input_text"
		if role == "assistant" {
			contentType = "output_text"
		}
		input = append(input, map[string]any{
			"type": "message",
			"role": role,
			"content": []map[string]string{
				{
					"type": contentType,
					"text": text,
				},
			},
		})
	}
	input = append(input, map[string]any{
		"type": "message",
		"role": "user",
		"content": []map[string]string{
			{
				"type": "input_text",
				"text": user,
			},
		},
	})
	return input
}

func (c *Client) loadHistory(sessionID string, limit int) []conversationMessage {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil
	}
	historyLimit := c.resolveContextLimit(limit)

	c.historyMu.Lock()
	cached := append([]conversationMessage(nil), c.history[sid]...)
	c.historyMu.Unlock()
	if len(cached) > historyLimit {
		return append([]conversationMessage(nil), cached[len(cached)-historyLimit:]...)
	}
	if len(cached) > 0 && c.memoryStore == nil {
		return cached
	}

	if c.memoryStore == nil {
		return cached
	}

	entries, err := c.memoryStore.LoadRecent(sid, historyLimit)
	if err != nil || len(entries) == 0 {
		return cached
	}
	loaded := make([]conversationMessage, 0, len(entries))
	for _, entry := range entries {
		role := strings.ToLower(strings.TrimSpace(entry.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			continue
		}
		loaded = append(loaded, conversationMessage{Role: role, Text: text})
	}
	if len(loaded) == 0 {
		return nil
	}
	if len(loaded) > historyLimit {
		loaded = append([]conversationMessage(nil), loaded[len(loaded)-historyLimit:]...)
	}

	c.historyMu.Lock()
	c.history[sid] = append([]conversationMessage(nil), loaded...)
	history := append([]conversationMessage(nil), loaded...)
	c.historyMu.Unlock()
	return history
}

func (c *Client) appendHistory(sessionID, userText, assistantText string) error {
	user := strings.TrimSpace(userText)
	assistant := strings.TrimSpace(assistantText)
	if user == "" || assistant == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	sid := strings.TrimSpace(sessionID)

	historyLimit := c.maxHistory
	if historyLimit <= 0 {
		historyLimit = defaultHistoryLimit
	}

	history := c.loadHistory(sid, 0)
	history = append(history, conversationMessage{Role: "user", Text: user})
	history = append(history, conversationMessage{Role: "assistant", Text: assistant})
	if len(history) > historyLimit {
		history = append([]conversationMessage(nil), history[len(history)-historyLimit:]...)
	}

	c.historyMu.Lock()
	c.history[sid] = history
	c.historyMu.Unlock()

	if c.memoryStore != nil {
		if err := c.memoryStore.Append(sid, "user", user); err != nil {
			return fmt.Errorf("persist user memory failed: %w", err)
		}
		if err := c.memoryStore.Append(sid, "assistant", assistant); err != nil {
			return fmt.Errorf("persist assistant memory failed: %w", err)
		}
	}
	return nil
}

func parseContextMessages(raw string) int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func parseMemoryRecallDays(raw string) int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func parseMaxOutputTokens(raw string) int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func parseOptionalBool(raw string) (bool, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func (c *Client) resolveContextLimit(requested int) int {
	if requested > 0 {
		return requested
	}
	if c.maxHistory > 0 {
		return c.maxHistory
	}
	return defaultHistoryLimit
}

func (c *Client) resolveRecallDays(requested int) int {
	if requested > 0 {
		return requested
	}
	return defaultRecallDays
}

func (c *Client) resolveRecallItemLimit(contextLimit int) int {
	if contextLimit <= 0 {
		return 1
	}
	limit := contextLimit / 4
	if limit < 1 {
		limit = 1
	}
	if limit > defaultRecallMaxItems {
		limit = defaultRecallMaxItems
	}
	return limit
}

func (c *Client) buildRelevantMemoryHints(
	userText string,
	history []conversationMessage,
	contextLimit int,
	recallDays int,
) (string, int) {
	result := c.buildRelevantMemoryHintsDetailed(userText, history, contextLimit, recallDays)
	return result.Hints, result.Stats.InjectedCount
}

func (c *Client) buildRelevantMemoryHintsDetailed(
	userText string,
	history []conversationMessage,
	contextLimit int,
	recallDays int,
) memoryHintBuildResult {
	result := memoryHintBuildResult{}
	if c.memoryStore == nil {
		return result
	}
	if recallDays <= 0 {
		return result
	}
	query := strings.TrimSpace(userText)
	if utf8Len(query) < minRecallQueryRunes {
		return result
	}
	limit := c.resolveRecallItemLimit(contextLimit)
	if limit <= 0 {
		return result
	}

	since := time.Now().AddDate(0, 0, -recallDays).UnixMilli()
	candidates, _, err := c.memoryStore.SearchPageSince("", query, defaultRecallCandidates, 0, since)
	if err != nil || len(candidates) == 0 {
		return result
	}
	result.Stats.Candidates = len(candidates)

	seen := make(map[string]struct{}, len(history)+1)
	for _, msg := range history {
		key := normalizeMemoryText(msg.Text)
		if key != "" {
			seen[key] = struct{}{}
		}
	}
	if key := normalizeMemoryText(query); key != "" {
		seen[key] = struct{}{}
	}

	lines := make([]string, 0, limit)
	totalRunes := 0
	sourceSessions := make(map[string]struct{}, len(candidates))
	for _, item := range candidates {
		if sid := strings.TrimSpace(item.SessionID); sid != "" {
			sourceSessions[sid] = struct{}{}
		}

		raw := compactWhitespace(item.Text)
		key := normalizeMemoryText(raw)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		snippet := truncateRunes(raw, defaultRecallItemMax)
		if snippet == "" {
			continue
		}
		result.Stats.InjectedRawRunes += utf8Len(raw)
		result.Stats.InjectedSnippetRunes += utf8Len(snippet)
		line := "- " + snippet
		lineRunes := utf8Len(line)
		if totalRunes+lineRunes > defaultRecallTotalMax {
			break
		}
		lines = append(lines, line)
		totalRunes += lineRunes
		if len(lines) >= limit {
			break
		}
	}
	if len(sourceSessions) > 0 {
		result.Stats.SourceSessions = make([]string, 0, len(sourceSessions))
		for sid := range sourceSessions {
			result.Stats.SourceSessions = append(result.Stats.SourceSessions, sid)
		}
		sort.Strings(result.Stats.SourceSessions)
	}
	if len(lines) == 0 {
		return result
	}

	result.Hints = fmt.Sprintf(
		"Relevant memory hints from the last %d days. Use only if directly relevant, and ignore stale details:\n%s",
		recallDays,
		strings.Join(lines, "\n"),
	)
	result.Stats.InjectedCount = len(lines)
	result.Stats.InjectedHintRunes = utf8Len(result.Hints)
	return result
}

func composeInstructions(base, model string) string {
	modelID := strings.TrimSpace(model)
	if modelID == "" {
		modelID = "unknown"
	}
	runtimeRule := fmt.Sprintf(
		"Runtime model id: %s. If asked about model/version, answer with this exact id and do not claim a different model.",
		modelID,
	)
	base = strings.TrimSpace(base)
	if base == "" {
		return runtimeRule
	}
	return base + "\n\n" + runtimeRule
}

func composeInstructionsWithMemoryHints(baseInstructions, memoryHints string) string {
	base := strings.TrimSpace(baseInstructions)
	hints := strings.TrimSpace(memoryHints)
	if hints == "" {
		return base
	}
	if base == "" {
		return hints
	}
	return base + "\n\n" + hints
}

func composeInstructionsWithConciseRule(baseInstructions string) string {
	base := strings.TrimSpace(baseInstructions)
	lower := strings.ToLower(base)
	if strings.Contains(lower, "be concise") {
		return base
	}
	if base == "" {
		return conciseInstructionRule
	}
	return base + "\n\n" + conciseInstructionRule
}

func composeInstructionsWithConciseRuleOption(baseInstructions string, concise, hasOverride bool) string {
	if hasOverride && !concise {
		return strings.TrimSpace(baseInstructions)
	}
	return composeInstructionsWithConciseRule(baseInstructions)
}

func resolveMaxOutputTokens(value int) int {
	if value > 0 {
		return value
	}
	return defaultMaxOutputTokens
}

func normalizeMemoryText(value string) string {
	return strings.ToLower(strings.TrimSpace(compactWhitespace(value)))
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func truncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-1]) + "…"
}

func utf8Len(value string) int {
	return len([]rune(value))
}
