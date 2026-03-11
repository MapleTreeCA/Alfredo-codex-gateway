package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gateway/internal/codexauth"
)

const (
	oauthStateTTL      = 10 * time.Minute
	maxAudioUploadSize = 20 << 20 // 20MB
	webSessionCookie   = "gateway_chat_sid"
)

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type oauthAuthFile struct {
	Type    string `json:"type"`
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires"`
}

type chatRequest struct {
	Text             string `json:"text"`
	SessionID        string `json:"session_id"`
	Model            string `json:"model"`
	Effort           string `json:"effort"`
	Verbosity        string `json:"verbosity"`
	Concise          *bool  `json:"concise"`
	MaxOutputTokens  *int   `json:"max_output_tokens"`
	ContextMessages  *int   `json:"context_messages"`
	MemoryRecallDays *int   `json:"memory_recall_days"`
	Online           *bool  `json:"online"`
}

type transcribeResponse struct {
	Text string `json:"text"`
}

type deviceSDConfigRequest struct {
	SessionID string          `json:"session_id"`
	Config    json.RawMessage `json:"config"`
	Reboot    bool            `json:"reboot"`
}

func (s *Server) HandleOAuthInitiate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	state, err := randomHex(16)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to create oauth state"})
		return
	}
	verifier, err := randomPKCEVerifier()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to create oauth verifier"})
		return
	}
	challenge := pkceChallenge(verifier)

	s.oauthMu.Lock()
	s.pruneOAuthStatesLocked()
	s.oauthStates[state] = oauthPendingState{
		Verifier:  verifier,
		ExpiresAt: time.Now().Add(oauthStateTTL),
	}
	s.oauthMu.Unlock()

	authURL, err := url.Parse(s.cfg.CodexOAuthAuthURL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "invalid oauth authorize url"})
		return
	}
	params := authURL.Query()
	params.Set("response_type", "code")
	params.Set("client_id", s.cfg.CodexOAuthClient)
	params.Set("redirect_uri", s.cfg.CodexOAuthRedirectURI)
	params.Set("scope", s.cfg.CodexOAuthScope)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)
	params.Set("id_token_add_organizations", "true")
	params.Set("codex_cli_simplified_flow", "true")
	params.Set("originator", "codex_cli_rs")
	authURL.RawQuery = params.Encode()

	if r.URL.Query().Get("redirect") == "1" {
		http.Redirect(w, r, authURL.String(), http.StatusFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"authorize_url": authURL.String(),
		"state":         state,
		"expires_in":    int(oauthStateTTL.Seconds()),
		"redirect_uri":  s.cfg.CodexOAuthRedirectURI,
	})
}

func (s *Server) HandleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	code := strings.TrimSpace(r.URL.Query().Get("code"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if code == "" || state == "" {
		s.renderOAuthResult(w, false, "missing code or state")
		return
	}

	verifier, ok := s.takeOAuthVerifier(state)
	if !ok {
		s.renderOAuthResult(w, false, "oauth state is invalid or expired")
		return
	}

	token, err := s.exchangeOAuthCode(r.Context(), code, verifier)
	if err != nil {
		s.renderOAuthResult(w, false, err.Error())
		return
	}

	auth := oauthAuthFile{
		Type:    "oauth",
		Access:  strings.TrimSpace(token.AccessToken),
		Refresh: strings.TrimSpace(token.RefreshToken),
		Expires: time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).UnixMilli(),
	}
	if auth.Access == "" || auth.Refresh == "" || auth.Expires <= 0 {
		s.renderOAuthResult(w, false, "oauth token response missing fields")
		return
	}

	if err := saveOAuthAuthFile(s.cfg.CodexAuthFile, auth); err != nil {
		s.renderOAuthResult(w, false, err.Error())
		return
	}

	accountID, _ := codexauth.ExtractAccountID(auth.Access)
	s.renderOAuthResult(w, true, accountID)
}

func (s *Server) HandleOAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := map[string]any{
		"provider":   s.cfg.LLMProvider,
		"auth_file":  s.cfg.CodexAuthFile,
		"authorized": false,
	}

	auth, err := loadOAuthAuthFile(s.cfg.CodexAuthFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			status["error"] = err.Error()
		}
		writeJSON(w, http.StatusOK, status)
		return
	}

	now := time.Now().UnixMilli()
	accountID, _ := codexauth.ExtractAccountID(auth.Access)
	expired := auth.Expires <= now
	status["authorized"] = auth.Type == "oauth" && auth.Access != "" && auth.Refresh != "" && !expired
	status["expires"] = auth.Expires
	status["expired"] = expired
	status["account_id"] = accountID
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) HandleRuntimeConfigAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		forceRefresh := r.URL.Query().Get("refresh") == "1"
		writeJSON(w, http.StatusOK, s.runtimeConfigResponse(r.Context(), forceRefresh))
		return
	case http.MethodPost:
		var req runtimeConfigPatchRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid runtime config payload"})
			return
		}
		if _, err := s.patchRuntimeConfig(req); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, s.runtimeConfigResponse(r.Context(), false))
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

func (s *Server) HandleChatAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid chat payload"})
		return
	}

	userText := strings.TrimSpace(req.Text)
	if userText == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text is empty"})
		return
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = sessionIDFromCookie(r)
	}
	if sessionID == "" {
		var err error
		sessionID, err = randomHex(12)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to create session id"})
			return
		}
		setSessionCookie(w, sessionID)
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.CodexTimeout+5*time.Second)
	defer cancel()

	runtime := s.getRuntimeConfig()
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = runtime.Model
	}
	effort := strings.TrimSpace(req.Effort)
	if effort == "" {
		effort = runtime.Effort
	}
	verbosity := strings.TrimSpace(req.Verbosity)
	if verbosity == "" {
		verbosity = runtime.Verbosity
	}
	online := runtime.Online
	if req.Online != nil {
		online = *req.Online
	}
	concise := runtime.Concise
	if req.Concise != nil {
		concise = *req.Concise
	}
	maxOutputTokens := runtime.MaxOutputTokens
	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 {
		maxOutputTokens = *req.MaxOutputTokens
	}
	contextMessages := runtime.ContextMessages
	if req.ContextMessages != nil && *req.ContextMessages > 0 {
		contextMessages = *req.ContextMessages
	}
	memoryRecallDays := runtime.MemoryRecallDays
	if req.MemoryRecallDays != nil && *req.MemoryRecallDays > 0 {
		memoryRecallDays = *req.MemoryRecallDays
	}

	options := map[string]string{
		"model":              model,
		"effort":             effort,
		"verbosity":          verbosity,
		"concise":            strconv.FormatBool(concise),
		"context_messages":   strconv.Itoa(contextMessages),
		"memory_recall_days": strconv.Itoa(memoryRecallDays),
		"online":             strconv.FormatBool(online),
	}

	var (
		reply          string
		usage          *codexauth.Usage
		memoryMessages int
		sentMessages   int
		err            error
	)
	if llmDetailed, ok := s.llm.(llmDetailedOptionClient); ok {
		result, detailedErr := llmDetailed.GenerateWithOptionsDetailed(ctx, sessionID, userText, options)
		err = detailedErr
		if detailedErr == nil {
			reply = result.Reply
			usage = &result.Usage
			memoryMessages = result.MemoryMessages
			sentMessages = result.SentMessages
		}
	} else if llmWithOptions, ok := s.llm.(llmOptionClient); ok {
		reply, err = llmWithOptions.GenerateWithOptions(ctx, sessionID, userText, options)
	} else {
		reply, err = s.llm.Generate(ctx, sessionID, userText)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	if sentMessages <= 0 {
		sentMessages = memoryMessages + 1
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":         sessionID,
		"provider":           s.cfg.LLMProvider,
		"model":              model,
		"effort":             effort,
		"verbosity":          verbosity,
		"concise":            concise,
		"max_output_tokens":  maxOutputTokens,
		"context_messages":   contextMessages,
		"memory_recall_days": memoryRecallDays,
		"memory_messages":    memoryMessages,
		"sent_messages":      sentMessages,
		"usage":              usage,
		"online":             online,
		"reply":              reply,
	})
}

func (s *Server) HandleTranscribeAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAudioUploadSize)
	if err := r.ParseMultipartForm(maxAudioUploadSize); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid multipart form"})
		return
	}

	file, _, err := r.FormFile("audio")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing audio file field: audio"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "failed to read audio"})
		return
	}
	if len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "audio is empty"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.STTTimeout+5*time.Second)
	defer cancel()

	text, err := s.transcriber.Transcribe(ctx, data)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, transcribeResponse{Text: text})
}

func (s *Server) HandleSynthesizeAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
		Rate  int    `json:"rate"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid tts payload"})
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text is empty"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.TTSTimeout+5*time.Second)
	defer cancel()

	runtime := s.getRuntimeConfig()
	voice := strings.TrimSpace(req.Voice)
	if voice == "" {
		voice = runtime.TTSVoice
	}
	rate := req.Rate
	if rate <= 0 {
		rate = runtime.TTSRate
	}

	var wav []byte
	var err error
	if synthWithOptions, ok := s.synthesizer.(speechOptionSynthesizer); ok {
		wav, err = synthWithOptions.SynthesizeWithOptions(ctx, text, map[string]string{
			"voice": voice,
			"rate":  strconv.Itoa(rate),
		})
	} else {
		wav, err = s.synthesizer.Synthesize(ctx, text)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("X-TTS-Voice", voice)
	w.Header().Set("X-TTS-Rate", strconv.Itoa(rate))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(wav)))
	_, _ = w.Write(wav)
}

func (s *Server) HandleConnectedDevicesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	devices := s.listConnectedDevices()
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": devices,
		"count":   len(devices),
	})
}

func (s *Server) HandleDeviceSDConfigAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req deviceSDConfigRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid device sd config payload"})
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id is required"})
		return
	}
	if len(req.Config) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "config is required"})
		return
	}

	var configObject map[string]any
	if err := json.Unmarshal(req.Config, &configObject); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "config must be a valid JSON object"})
		return
	}
	if configObject == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "config must be a JSON object"})
		return
	}

	if err := s.sendSystemCommandToDevice(
		sessionID,
		"write_sdcard_runtime_config",
		configObject,
		req.Reboot,
	); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session_id": sessionID,
		"reboot":     req.Reboot,
		"queued":     true,
	})
}

func (s *Server) exchangeOAuthCode(ctx context.Context, code, verifier string) (oauthTokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {s.cfg.CodexOAuthClient},
		"code":          {strings.TrimSpace(code)},
		"code_verifier": {strings.TrimSpace(verifier)},
		"redirect_uri":  {s.cfg.CodexOAuthRedirectURI},
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		s.cfg.CodexOAuthToken,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return oauthTokenResponse{}, fmt.Errorf("build oauth token request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return oauthTokenResponse{}, fmt.Errorf("oauth token request failed: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return oauthTokenResponse{}, fmt.Errorf("read oauth token response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oauthTokenResponse{}, fmt.Errorf(
			"oauth token error (%d): %s",
			resp.StatusCode,
			strings.TrimSpace(string(payload)),
		)
	}

	var out oauthTokenResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return oauthTokenResponse{}, fmt.Errorf("parse oauth token response failed: %w", err)
	}
	return out, nil
}

func (s *Server) renderOAuthResult(w http.ResponseWriter, ok bool, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if ok {
		_, _ = fmt.Fprintf(
			w,
			`<!doctype html><html><body><h3>OAuth success</h3><p>Account: %s</p><script>setTimeout(()=>window.location.href="/",1000)</script></body></html>`,
			htmlEscape(detail),
		)
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	_, _ = fmt.Fprintf(
		w,
		`<!doctype html><html><body><h3>OAuth failed</h3><p>%s</p><p><a href="/">Back</a></p></body></html>`,
		htmlEscape(detail),
	)
}

func (s *Server) takeOAuthVerifier(state string) (string, bool) {
	now := time.Now()
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	s.pruneOAuthStatesLocked()
	entry, ok := s.oauthStates[state]
	if !ok {
		return "", false
	}
	delete(s.oauthStates, state)
	if entry.ExpiresAt.Before(now) {
		return "", false
	}
	return entry.Verifier, true
}

func (s *Server) pruneOAuthStatesLocked() {
	now := time.Now()
	for state, value := range s.oauthStates {
		if value.ExpiresAt.Before(now) {
			delete(s.oauthStates, state)
		}
	}
}

func saveOAuthAuthFile(path string, auth oauthAuthFile) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("CODEX_AUTH_FILE is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create auth dir failed: %w", err)
	}
	body, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal oauth auth file failed: %w", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write oauth auth file failed: %w", err)
	}
	return nil
}

func loadOAuthAuthFile(path string) (oauthAuthFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return oauthAuthFile{}, err
	}
	var out oauthAuthFile
	if err := json.Unmarshal(raw, &out); err != nil {
		return oauthAuthFile{}, fmt.Errorf("parse oauth auth file failed: %w", err)
	}
	return out, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func randomPKCEVerifier() (string, error) {
	buf := make([]byte, 48)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func sessionIDFromCookie(r *http.Request) string {
	cookie, err := r.Cookie(webSessionCookie)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func setSessionCookie(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     webSessionCookie,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((24 * time.Hour).Seconds()),
	})
}

func htmlEscape(input string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(strings.TrimSpace(input))
}
