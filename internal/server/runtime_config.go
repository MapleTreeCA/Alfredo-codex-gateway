package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"gateway/internal/codexauth"
	"gateway/internal/config"
	"gateway/internal/memorystore"
)

var (
	defaultModelOptions = []string{
		"gpt-5.4",
		"gpt-5.3",
		"gpt-5.2-codex",
		"gpt-5.2",
		"gpt-5.1-codex",
		"gpt-5.1-codex-max",
		"gpt-5.1-codex-mini",
		"gpt-5.1",
		"gpt-5",
		"o4-mini",
		"o3",
		"o3-mini",
	}
	defaultEffortOptions = []string{"low", "medium", "high"}
)

const (
	defaultCodexClientVersion = "0.1.0"
	modelCatalogCacheTTL      = 5 * time.Minute
	defaultContextMessages    = 10
	maxContextMessages        = 200
	defaultMemoryRecallDays   = 30
	maxMemoryRecallDays       = 365
	defaultConcise            = true
	defaultMaxOutputTokens    = 1000
	minMaxOutputTokens        = 1
	maxMaxOutputTokens        = 8192
	// End a turn after this much trailing silence, then run final STT -> LLM -> TTS.
	defaultSessionSilenceMS = 900
	// Hard-stop a turn at this length even if speech/noise keeps going.
	defaultSessionMaxTurnMS = 12000
	// Minimum wall-clock gap between interim STT requests while streaming.
	defaultSTTInterimIntervalMS = 600
	// Minimum captured audio before interim STT can trigger.
	defaultSTTInterimMinAudioMS = 700
	minSessionSilenceMS         = 200
	maxSessionSilenceMS         = 10000
	minSessionMaxTurnMS         = 2000
	maxSessionMaxTurnMS         = 120000
	minSTTInterimIntervalMS     = 200
	maxSTTInterimIntervalMS     = 10000
	minSTTInterimMinAudioMS     = 200
	maxSTTInterimMinAudioMS     = 30000
)

type runtimeConfig struct {
	// Model is the Codex/LLM model slug used for final text generation.
	Model string `json:"model"`
	// Effort controls the reasoning depth requested from the model.
	Effort string `json:"effort"`
	// Verbosity controls how wordy the model is allowed to be.
	Verbosity string `json:"verbosity"`
	// Online enables tools/web-backed behavior when the model/provider supports it.
	Online bool `json:"online"`
	// Concise asks the model to keep answers shorter by default.
	Concise bool `json:"concise"`
	// MaxOutputTokens caps model text output before TTS sees it.
	MaxOutputTokens int `json:"max_output_tokens"`
	// ContextMessages is the number of recent dialogue messages sent to the LLM.
	ContextMessages int `json:"context_messages"`
	// MemoryRecallDays limits how many days of stored memory can be recalled into context.
	MemoryRecallDays int `json:"memory_recall_days"`
	// TTSVoice is the voice name used by the configured TTS provider.
	TTSVoice string `json:"tts_voice"`
	// TTSRate is the speech rate sent to the TTS provider.
	TTSRate int `json:"tts_rate"`
	// SessionSilenceMS is the silence window that finalizes a turn and sends it to STT/LLM.
	SessionSilenceMS int `json:"session_silence_ms"`
	// SessionMaxTurnMS is the hard upper bound for one turn of captured audio.
	SessionMaxTurnMS int `json:"session_max_turn_ms"`
	// STTStreamingEnabled enables interim STT updates while the user is still speaking.
	STTStreamingEnabled bool `json:"stt_streaming_enabled"`
	// STTInterimIntervalMS is the minimum time gap between interim STT requests.
	STTInterimIntervalMS int `json:"stt_interim_interval_ms"`
	// STTInterimMinAudioMS is the minimum buffered audio required before interim STT can run.
	STTInterimMinAudioMS int `json:"stt_interim_min_audio_ms"`
}

// runtimeConfigPatchRequest uses pointer fields so HTTP PATCH can omit untouched values.
// Field meanings are identical to runtimeConfig.
type runtimeConfigPatchRequest struct {
	Model                *string `json:"model"`
	Effort               *string `json:"effort"`
	Verbosity            *string `json:"verbosity"`
	Online               *bool   `json:"online"`
	Concise              *bool   `json:"concise"`
	MaxOutputTokens      *int    `json:"max_output_tokens"`
	ContextMessages      *int    `json:"context_messages"`
	MemoryRecallDays     *int    `json:"memory_recall_days"`
	TTSVoice             *string `json:"tts_voice"`
	TTSRate              *int    `json:"tts_rate"`
	SessionSilenceMS     *int    `json:"session_silence_ms"`
	SessionMaxTurnMS     *int    `json:"session_max_turn_ms"`
	STTStreamingEnabled  *bool   `json:"stt_streaming_enabled"`
	STTInterimIntervalMS *int    `json:"stt_interim_interval_ms"`
	STTInterimMinAudioMS *int    `json:"stt_interim_min_audio_ms"`
}

// runtimeConfigResponse returns the active config plus UI option catalogs.
// Field meanings are identical to runtimeConfig unless noted otherwise.
type runtimeConfigResponse struct {
	Model                string `json:"model"`
	Effort               string `json:"effort"`
	Verbosity            string `json:"verbosity"`
	Online               bool   `json:"online"`
	Concise              bool   `json:"concise"`
	MaxOutputTokens      int    `json:"max_output_tokens"`
	ContextMessages      int    `json:"context_messages"`
	MemoryRecallDays     int    `json:"memory_recall_days"`
	TTSVoice             string `json:"tts_voice"`
	TTSRate              int    `json:"tts_rate"`
	SessionSilenceMS     int    `json:"session_silence_ms"`
	SessionMaxTurnMS     int    `json:"session_max_turn_ms"`
	STTStreamingEnabled  bool   `json:"stt_streaming_enabled"`
	STTInterimIntervalMS int    `json:"stt_interim_interval_ms"`
	STTInterimMinAudioMS int    `json:"stt_interim_min_audio_ms"`
	// ModelOptions is the selectable model list exposed to the runtime config UI.
	ModelOptions []string `json:"model_options"`
	// ModelOptionsSource tells whether model options came from static defaults or live fetch.
	ModelOptionsSource string   `json:"model_options_source"`
	EffortOptions      []string `json:"effort_options"`
	VerbosityOptions   []string `json:"verbosity_options"`
	VoiceOptions       []string `json:"voice_options"`
	ENVoiceOptions     []string `json:"en_voice_options"`
	RecommendedENMales []string `json:"recommended_en_male_voices"`
}

type voiceCatalog struct {
	All    []string
	EN     []string
	ENMale []string
}

type modelCatalog struct {
	Options   []string
	UpdatedAt time.Time
	Source    string
}

func defaultRuntimeConfig(cfg config.Config) runtimeConfig {
	maxOutputTokens := cfg.CodexMaxOutputTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = defaultMaxOutputTokens
	}
	contextMessages := cfg.MemoryContextSize
	if contextMessages <= 0 {
		contextMessages = defaultContextMessages
	}
	if contextMessages > maxContextMessages {
		contextMessages = maxContextMessages
	}
	sessionSilenceMS := int(cfg.SessionSilence / time.Millisecond)
	if sessionSilenceMS <= 0 {
		sessionSilenceMS = defaultSessionSilenceMS
	}
	sessionMaxTurnMS := int(cfg.SessionMaxTurn / time.Millisecond)
	if sessionMaxTurnMS <= 0 {
		sessionMaxTurnMS = defaultSessionMaxTurnMS
	}
	sttInterimIntervalMS := int(cfg.STTInterimInterval / time.Millisecond)
	if sttInterimIntervalMS <= 0 {
		sttInterimIntervalMS = defaultSTTInterimIntervalMS
	}
	sttInterimMinAudioMS := int(cfg.STTInterimMinAudio / time.Millisecond)
	if sttInterimMinAudioMS <= 0 {
		sttInterimMinAudioMS = defaultSTTInterimMinAudioMS
	}
	return sanitizeRuntimeConfig(runtimeConfig{
		Model:                strings.TrimSpace(cfg.CodexModel),
		Effort:               "medium",
		Verbosity:            "medium",
		Online:               true,
		Concise:              defaultConcise,
		MaxOutputTokens:      maxOutputTokens,
		ContextMessages:      contextMessages,
		MemoryRecallDays:     defaultMemoryRecallDays,
		TTSVoice:             strings.TrimSpace(cfg.LocalTTSVoice),
		TTSRate:              cfg.LocalTTSRate,
		SessionSilenceMS:     sessionSilenceMS,
		SessionMaxTurnMS:     sessionMaxTurnMS,
		STTStreamingEnabled:  cfg.STTStreamingEnabled,
		STTInterimIntervalMS: sttInterimIntervalMS,
		STTInterimMinAudioMS: sttInterimMinAudioMS,
	})
}

func (s *Server) getRuntimeConfig() runtimeConfig {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.runtime
}

func (s *Server) patchRuntimeConfig(req runtimeConfigPatchRequest) (runtimeConfig, error) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	next := mergeRuntimeConfig(s.runtime, req)
	if err := saveRuntimeConfigToStore(s.memoryStore, next); err != nil {
		return s.runtime, err
	}
	s.runtime = next
	return next, nil
}

func mergeRuntimeConfig(current runtimeConfig, req runtimeConfigPatchRequest) runtimeConfig {
	next := current
	if req.Model != nil {
		if value := strings.TrimSpace(*req.Model); value != "" {
			next.Model = value
		}
	}
	if req.Effort != nil {
		next.Effort = strings.ToLower(strings.TrimSpace(*req.Effort))
	}
	if req.Verbosity != nil {
		next.Verbosity = strings.ToLower(strings.TrimSpace(*req.Verbosity))
	}
	if req.Online != nil {
		next.Online = *req.Online
	}
	if req.Concise != nil {
		next.Concise = *req.Concise
	}
	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 {
		next.MaxOutputTokens = *req.MaxOutputTokens
	}
	if req.ContextMessages != nil && *req.ContextMessages > 0 {
		next.ContextMessages = *req.ContextMessages
	}
	if req.MemoryRecallDays != nil && *req.MemoryRecallDays > 0 {
		next.MemoryRecallDays = *req.MemoryRecallDays
	}
	if req.TTSVoice != nil {
		next.TTSVoice = strings.TrimSpace(*req.TTSVoice)
	}
	if req.TTSRate != nil && *req.TTSRate > 0 {
		next.TTSRate = *req.TTSRate
	}
	if req.SessionSilenceMS != nil && *req.SessionSilenceMS > 0 {
		next.SessionSilenceMS = *req.SessionSilenceMS
	}
	if req.SessionMaxTurnMS != nil && *req.SessionMaxTurnMS > 0 {
		next.SessionMaxTurnMS = *req.SessionMaxTurnMS
	}
	if req.STTStreamingEnabled != nil {
		next.STTStreamingEnabled = *req.STTStreamingEnabled
	}
	if req.STTInterimIntervalMS != nil && *req.STTInterimIntervalMS > 0 {
		next.STTInterimIntervalMS = *req.STTInterimIntervalMS
	}
	if req.STTInterimMinAudioMS != nil && *req.STTInterimMinAudioMS > 0 {
		next.STTInterimMinAudioMS = *req.STTInterimMinAudioMS
	}
	return sanitizeRuntimeConfig(next)
}

func sanitizeRuntimeConfig(next runtimeConfig) runtimeConfig {
	next.Model = strings.TrimSpace(next.Model)
	if next.Model == "" {
		next.Model = "gpt-5.1-codex"
	}
	switch strings.ToLower(strings.TrimSpace(next.Effort)) {
	case "low", "medium", "high":
		next.Effort = strings.ToLower(strings.TrimSpace(next.Effort))
	default:
		next.Effort = "medium"
	}
	next.Verbosity = codexauth.NormalizeTextVerbosityForModel(next.Model, next.Verbosity)
	next.MaxOutputTokens = clampInt(
		next.MaxOutputTokens,
		defaultMaxOutputTokens,
		minMaxOutputTokens,
		maxMaxOutputTokens,
	)
	if next.ContextMessages <= 0 {
		next.ContextMessages = defaultContextMessages
	}
	if next.ContextMessages > maxContextMessages {
		next.ContextMessages = maxContextMessages
	}
	if next.MemoryRecallDays <= 0 {
		next.MemoryRecallDays = defaultMemoryRecallDays
	}
	if next.MemoryRecallDays > maxMemoryRecallDays {
		next.MemoryRecallDays = maxMemoryRecallDays
	}
	next.TTSVoice = strings.TrimSpace(next.TTSVoice)
	if next.TTSVoice == "" {
		next.TTSVoice = "Daniel"
	}
	if next.TTSRate <= 0 {
		next.TTSRate = 180
	}
	next.SessionSilenceMS = clampInt(next.SessionSilenceMS, defaultSessionSilenceMS, minSessionSilenceMS, maxSessionSilenceMS)
	next.SessionMaxTurnMS = clampInt(next.SessionMaxTurnMS, defaultSessionMaxTurnMS, minSessionMaxTurnMS, maxSessionMaxTurnMS)
	next.STTInterimIntervalMS = clampInt(
		next.STTInterimIntervalMS,
		defaultSTTInterimIntervalMS,
		minSTTInterimIntervalMS,
		maxSTTInterimIntervalMS,
	)
	next.STTInterimMinAudioMS = clampInt(
		next.STTInterimMinAudioMS,
		defaultSTTInterimMinAudioMS,
		minSTTInterimMinAudioMS,
		maxSTTInterimMinAudioMS,
	)
	return next
}

func clampInt(value, fallback, minValue, maxValue int) int {
	if value <= 0 {
		value = fallback
	}
	if value < minValue {
		value = minValue
	}
	if value > maxValue {
		value = maxValue
	}
	return value
}

func loadRuntimeConfigFromStore(store *memorystore.Store, fallback runtimeConfig) (runtimeConfig, bool, error) {
	if store == nil {
		return sanitizeRuntimeConfig(fallback), false, nil
	}
	payload, err := store.LoadRuntimeConfig()
	if err != nil {
		return sanitizeRuntimeConfig(fallback), false, fmt.Errorf("load runtime config from sqlite failed: %w", err)
	}
	value := strings.TrimSpace(string(payload))
	if value == "" {
		return sanitizeRuntimeConfig(fallback), false, nil
	}
	var patch runtimeConfigPatchRequest
	if err := json.Unmarshal([]byte(value), &patch); err != nil {
		return sanitizeRuntimeConfig(fallback), false, fmt.Errorf("decode runtime config from sqlite failed: %w", err)
	}
	return mergeRuntimeConfig(sanitizeRuntimeConfig(fallback), patch), true, nil
}

func saveRuntimeConfigToStore(store *memorystore.Store, cfg runtimeConfig) error {
	if store == nil {
		return nil
	}
	payload, err := json.Marshal(sanitizeRuntimeConfig(cfg))
	if err != nil {
		return fmt.Errorf("encode runtime config failed: %w", err)
	}
	if err := store.SaveRuntimeConfig(payload); err != nil {
		return fmt.Errorf("save runtime config to sqlite failed: %w", err)
	}
	return nil
}

func (s *Server) runtimeConfigResponse(ctx context.Context, forceRefresh bool) runtimeConfigResponse {
	current := s.getRuntimeConfig()
	models, source := s.getModelOptions(ctx, forceRefresh)
	return runtimeConfigResponse{
		Model:                current.Model,
		Effort:               current.Effort,
		Verbosity:            current.Verbosity,
		Online:               current.Online,
		Concise:              current.Concise,
		MaxOutputTokens:      current.MaxOutputTokens,
		ContextMessages:      current.ContextMessages,
		MemoryRecallDays:     current.MemoryRecallDays,
		TTSVoice:             current.TTSVoice,
		TTSRate:              current.TTSRate,
		SessionSilenceMS:     current.SessionSilenceMS,
		SessionMaxTurnMS:     current.SessionMaxTurnMS,
		STTStreamingEnabled:  current.STTStreamingEnabled,
		STTInterimIntervalMS: current.STTInterimIntervalMS,
		STTInterimMinAudioMS: current.STTInterimMinAudioMS,
		ModelOptions:         models,
		ModelOptionsSource:   source,
		EffortOptions:        append([]string(nil), defaultEffortOptions...),
		VerbosityOptions:     codexauth.SupportedTextVerbosityOptions(current.Model),
		VoiceOptions:         append([]string(nil), s.voiceCatalog.All...),
		ENVoiceOptions:       append([]string(nil), s.voiceCatalog.EN...),
		RecommendedENMales:   append([]string(nil), s.voiceCatalog.ENMale...),
	}
}

func (s *Server) getModelOptions(ctx context.Context, forceRefresh bool) ([]string, string) {
	s.modelMu.Lock()
	cached := s.modelCatalog
	s.modelMu.Unlock()

	if !forceRefresh && len(cached.Options) > 0 && time.Since(cached.UpdatedAt) < modelCatalogCacheTTL {
		return append([]string(nil), cached.Options...), cached.Source
	}

	options, source, err := s.fetchModelOptions(ctx)
	if err != nil {
		if len(cached.Options) > 0 {
			return append([]string(nil), cached.Options...), cached.Source
		}
		return append([]string(nil), defaultModelOptions...), "static"
	}

	s.modelMu.Lock()
	s.modelCatalog = modelCatalog{
		Options:   append([]string(nil), options...),
		UpdatedAt: time.Now(),
		Source:    source,
	}
	s.modelMu.Unlock()
	return append([]string(nil), options...), source
}

func (s *Server) fetchModelOptions(ctx context.Context) ([]string, string, error) {
	if s.cfg.LLMProvider != "codex" {
		return append([]string(nil), defaultModelOptions...), "static", nil
	}

	auth, err := loadOAuthAuthFile(s.cfg.CodexAuthFile)
	if err != nil || strings.TrimSpace(auth.Access) == "" {
		return append([]string(nil), defaultModelOptions...), "static", nil
	}

	accountID, err := codexauth.ExtractAccountID(auth.Access)
	if err != nil || strings.TrimSpace(accountID) == "" {
		return append([]string(nil), defaultModelOptions...), "static", nil
	}

	clientVersion := strings.TrimSpace(os.Getenv("GATEWAY_CODEX_CLIENT_VERSION"))
	if clientVersion == "" {
		clientVersion = defaultCodexClientVersion
	}

	base := strings.TrimRight(strings.TrimSpace(s.cfg.CodexBaseURL), "/")
	if base == "" {
		base = "https://chatgpt.com/backend-api"
	}
	endpoint := base + "/codex/models?client_version=" + url.QueryEscape(clientVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+auth.Access)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("chatgpt-account-id", accountID)
	req.Header.Set("originator", "codex_cli_rs")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return append([]string(nil), defaultModelOptions...), "static", nil
	}

	var out struct {
		Models []struct {
			Slug           string `json:"slug"`
			SupportedInAPI bool   `json:"supported_in_api"`
			Visibility     string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, "", err
	}

	options := make([]string, 0, len(out.Models))
	seen := make(map[string]struct{})
	for _, item := range out.Models {
		slug := strings.TrimSpace(item.Slug)
		if slug == "" {
			continue
		}
		if !item.SupportedInAPI {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.Visibility), "hidden") {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		options = append(options, slug)
	}
	for _, fallback := range defaultModelOptions {
		if _, ok := seen[fallback]; !ok {
			options = append(options, fallback)
			seen[fallback] = struct{}{}
		}
	}
	if len(options) == 0 {
		return append([]string(nil), defaultModelOptions...), "static", nil
	}
	return options, "codex-api", nil
}

func loadVoiceCatalog() voiceCatalog {
	if _, err := exec.LookPath("say"); err != nil {
		return voiceCatalog{}
	}
	out, err := exec.Command("say", "-v", "?").Output()
	if err != nil {
		return voiceCatalog{}
	}
	re := regexp.MustCompile(`^\s*(.+?)\s+([a-z]{2}_[A-Z]{2})\s+#`)

	all := make([]string, 0, 128)
	en := make([]string, 0, 48)
	enSet := make(map[string]struct{})
	allSet := make(map[string]struct{})

	for _, line := range strings.Split(string(out), "\n") {
		m := re.FindStringSubmatch(line)
		if len(m) != 3 {
			continue
		}
		name := strings.TrimSpace(m[1])
		locale := strings.TrimSpace(m[2])
		if name == "" {
			continue
		}
		if _, ok := allSet[name]; !ok {
			allSet[name] = struct{}{}
			all = append(all, name)
		}
		if strings.HasPrefix(locale, "en_") {
			if _, ok := enSet[name]; !ok {
				enSet[name] = struct{}{}
				en = append(en, name)
			}
		}
	}
	sort.Strings(all)
	sort.Strings(en)

	preferred := []string{
		"Daniel",
		"Reed",
		"Eddy (English (United States))",
		"Eddy",
		"Albert",
		"Fred",
		"Ralph",
		"Rishi",
		"Grandpa",
	}
	males := make([]string, 0, len(preferred))
	for _, name := range en {
		if hasAnyPrefix(name, preferred) {
			males = append(males, name)
		}
	}
	if len(males) == 0 {
		for _, fallback := range []string{"Daniel", "Reed", "Albert", "Fred", "Ralph", "Rishi"} {
			if slices.Contains(en, fallback) {
				males = append(males, fallback)
			}
		}
	}

	return voiceCatalog{
		All:    all,
		EN:     en,
		ENMale: males,
	}
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(value, p) {
			return true
		}
	}
	return false
}
