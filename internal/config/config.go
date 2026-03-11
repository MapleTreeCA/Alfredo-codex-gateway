package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenAddr         = ":18910"
	defaultWSPath             = "/ws"
	defaultLLMProvider        = "codex"
	defaultSTTProvider        = "openai"
	defaultTTSProvider        = "openai"
	defaultLocalSTTAddr       = "127.0.0.1:19610"
	defaultLocalTTSAddr       = "127.0.0.1:19611"
	defaultLocalModuleWait    = 10 * time.Second
	defaultOpenAIBaseURL      = "https://api.openai.com/v1"
	defaultCodexBaseURL       = "https://chatgpt.com/backend-api"
	defaultCodexOAuthClient   = "app_EMoamEEZ73f0CkXaXp7hrann"
	defaultCodexOAuthAuthURL  = "https://auth.openai.com/oauth/authorize"
	defaultCodexOAuthToken    = "https://auth.openai.com/oauth/token"
	defaultCodexOAuthScope    = "openid profile email offline_access"
	defaultOpenClawURL        = "ws://127.0.0.1:18789"
	defaultOpenClawSession    = "alfredo"
	defaultSTTModel           = "gpt-4o-mini-transcribe"
	defaultMLXWhisperModel    = "mlx-community/whisper-large-v3-turbo"
	defaultTTSModel           = "tts-1"
	defaultTTSVoice           = "alloy"
	defaultLocalTTSVoice      = "Daniel"
	defaultLocalTTSRate       = 220
	defaultCodexModel         = "gpt-5.1-codex"
	defaultCodexMaxOutput     = 500
	defaultOpusFrameDuration  = 60
	defaultSessionSilence     = 1200 * time.Millisecond
	defaultSessionMaxTurn     = 15 * time.Second
	defaultTTSMaxDuration     = 3 * time.Minute
	defaultSTTStreaming       = true
	defaultSTTInterimInterval = 900 * time.Millisecond
	defaultSTTInterimMinAudio = 1200 * time.Millisecond
	defaultOAuthRedirectURI   = "http://localhost:1455/auth/callback"
	defaultSTTTimeout         = 45 * time.Second
	defaultTTSTimeout         = 45 * time.Second
	defaultCodexTimeout       = 90 * time.Second
	defaultDownlinkRate       = 24000
	defaultDownlinkBitrate    = 24000
	defaultMemoryContextSize  = 10
)

type Config struct {
	ListenAddr          string
	WSPath              string
	WSToken             string
	LLMProvider         string
	STTProvider         string
	TTSProvider         string
	SessionSilence      time.Duration
	SessionMaxTurn      time.Duration
	TTSMaxDuration      time.Duration
	STTStreamingEnabled bool
	STTInterimInterval  time.Duration
	STTInterimMinAudio  time.Duration
	DownlinkSampleRate  int
	DownlinkOpusBitrate int
	OpusFrameDuration   int
	MemoryDir           string
	MemoryContextSize   int

	OpenAIAPIKey              string
	OpenAIBaseURL             string
	STTModel                  string
	STTLanguage               string
	STTTimeout                time.Duration
	MLXWhisperBin             string
	MLXWhisperModel           string
	LocalSTTAddr              string
	TTSModel                  string
	TTSVoice                  string
	TTSTimeout                time.Duration
	LocalTTSAddr              string
	LocalTTSVoice             string
	LocalTTSRate              int
	LocalTTSSampleRate        int
	LocalModuleStartupTimeout time.Duration

	OpenClawURL         string
	OpenClawToken       string
	OpenClawSessionKey  string
	OpenClawAgentID     string
	OpenClawDialTimeout time.Duration

	CodexAuthFile         string
	CodexBaseURL          string
	CodexModel            string
	CodexMaxOutputTokens  int
	CodexTimeout          time.Duration
	CodexInstructions     string
	CodexOAuthClient      string
	CodexOAuthAuthURL     string
	CodexOAuthToken       string
	CodexOAuthRedirectURI string
	CodexOAuthScope       string
}

func Load() Config {
	llmProvider := normalizeLLMProvider(envString("GATEWAY_LLM_PROVIDER", defaultLLMProvider))
	sttProvider := normalizeSpeechProvider(strings.TrimSpace(os.Getenv("GATEWAY_STT_PROVIDER")))
	if sttProvider == "" {
		if llmProvider == "openclaw" {
			sttProvider = "openclaw"
		} else {
			sttProvider = defaultSTTProvider
		}
	}
	ttsProvider := normalizeSpeechProvider(strings.TrimSpace(os.Getenv("GATEWAY_TTS_PROVIDER")))
	if ttsProvider == "" {
		if llmProvider == "openclaw" {
			ttsProvider = "openclaw"
		} else {
			ttsProvider = defaultTTSProvider
		}
	}
	needsOpenClaw := llmProvider == "openclaw" || sttProvider == "openclaw" || ttsProvider == "openclaw"
	openClawURL := envString("GATEWAY_OPENCLAW_URL", defaultOpenClawURL)
	if needsOpenClaw {
		if _, ok := os.LookupEnv("GATEWAY_OPENCLAW_URL"); !ok {
			if port := discoverOpenClawConfig("gateway.port"); port != "" {
				openClawURL = "ws://127.0.0.1:" + port
			}
		}
	}
	openClawToken := strings.TrimSpace(os.Getenv("GATEWAY_OPENCLAW_TOKEN"))
	if needsOpenClaw && openClawToken == "" {
		openClawToken = discoverOpenClawToken()
	}
	sttLanguage := strings.TrimSpace(os.Getenv("GATEWAY_STT_LANGUAGE"))
	if sttLanguage == "" {
		sttLanguage = strings.TrimSpace(os.Getenv("OPENAI_STT_LANGUAGE"))
	}

	return Config{
		ListenAddr:          envString("GATEWAY_LISTEN_ADDR", defaultListenAddr),
		WSPath:              normalizePath(envString("GATEWAY_WS_PATH", defaultWSPath)),
		WSToken:             strings.TrimSpace(os.Getenv("GATEWAY_WS_TOKEN")),
		LLMProvider:         llmProvider,
		STTProvider:         sttProvider,
		TTSProvider:         ttsProvider,
		SessionSilence:      envDuration("GATEWAY_SESSION_SILENCE", defaultSessionSilence),
		SessionMaxTurn:      envDuration("GATEWAY_SESSION_MAX_TURN", defaultSessionMaxTurn),
		TTSMaxDuration:      envDuration("GATEWAY_TTS_MAX_DURATION", defaultTTSMaxDuration),
		STTStreamingEnabled: envBool("GATEWAY_STT_STREAMING_ENABLED", defaultSTTStreaming),
		STTInterimInterval:  envDuration("GATEWAY_STT_INTERIM_INTERVAL", defaultSTTInterimInterval),
		STTInterimMinAudio:  envDuration("GATEWAY_STT_INTERIM_MIN_AUDIO", defaultSTTInterimMinAudio),
		DownlinkSampleRate:  envInt("GATEWAY_DOWNLINK_SAMPLE_RATE", defaultDownlinkRate),
		DownlinkOpusBitrate: envInt("GATEWAY_DOWNLINK_OPUS_BITRATE", defaultDownlinkBitrate),
		OpusFrameDuration:   envInt("GATEWAY_OPUS_FRAME_DURATION_MS", defaultOpusFrameDuration),
		MemoryDir:           envString("GATEWAY_MEMORY_DIR", defaultMemoryDir()),
		MemoryContextSize:   envInt("GATEWAY_MEMORY_CONTEXT_SIZE", defaultMemoryContextSize),
		OpenAIAPIKey:        strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		OpenAIBaseURL:       strings.TrimRight(envString("OPENAI_BASE_URL", defaultOpenAIBaseURL), "/"),
		STTModel:            envString("OPENAI_STT_MODEL", defaultSTTModel),
		STTLanguage:         sttLanguage,
		STTTimeout:          envDuration("OPENAI_STT_TIMEOUT", defaultSTTTimeout),
		MLXWhisperBin:       envString("GATEWAY_MLX_WHISPER_BIN", "mlx_whisper"),
		MLXWhisperModel:     envString("GATEWAY_MLX_WHISPER_MODEL", defaultMLXWhisperModel),
		LocalSTTAddr:        envString("GATEWAY_LOCAL_STT_ADDR", defaultLocalSTTAddr),
		TTSModel:            envString("OPENAI_TTS_MODEL", defaultTTSModel),
		TTSVoice:            envString("OPENAI_TTS_VOICE", defaultTTSVoice),
		TTSTimeout:          envDuration("OPENAI_TTS_TIMEOUT", defaultTTSTimeout),
		LocalTTSAddr:        envString("GATEWAY_LOCAL_TTS_ADDR", defaultLocalTTSAddr),
		LocalTTSVoice:       envString("GATEWAY_LOCAL_TTS_VOICE", defaultLocalTTSVoice),
		LocalTTSRate:        envInt("GATEWAY_LOCAL_TTS_RATE", defaultLocalTTSRate),
		LocalTTSSampleRate:  envInt("GATEWAY_LOCAL_TTS_SAMPLE_RATE", defaultDownlinkRate),
		LocalModuleStartupTimeout: envDuration(
			"GATEWAY_LOCAL_MODULE_STARTUP_TIMEOUT",
			defaultLocalModuleWait,
		),
		OpenClawURL:        openClawURL,
		OpenClawToken:      openClawToken,
		OpenClawSessionKey: envString("GATEWAY_OPENCLAW_SESSION_KEY", defaultOpenClawSession),
		OpenClawAgentID:    strings.TrimSpace(os.Getenv("GATEWAY_OPENCLAW_AGENT_ID")),
		OpenClawDialTimeout: envSeconds(
			"GATEWAY_OPENCLAW_DIAL_TIMEOUT_SEC",
			10*time.Second,
		),
		CodexAuthFile:         defaultAuthFile(),
		CodexBaseURL:          strings.TrimRight(envString("CODEX_BASE_URL", defaultCodexBaseURL), "/"),
		CodexModel:            envString("CODEX_MODEL", defaultCodexModel),
		CodexMaxOutputTokens:  envInt("CODEX_MAX_OUTPUT_TOKENS", defaultCodexMaxOutput),
		CodexTimeout:          envDuration("CODEX_TIMEOUT", defaultCodexTimeout),
		CodexInstructions:     defaultInstructions(strings.TrimSpace(os.Getenv("CODEX_SYSTEM_PROMPT"))),
		CodexOAuthClient:      envString("CODEX_OAUTH_CLIENT_ID", defaultCodexOAuthClient),
		CodexOAuthAuthURL:     strings.TrimRight(envString("CODEX_OAUTH_AUTHORIZE_URL", defaultCodexOAuthAuthURL), "/"),
		CodexOAuthToken:       strings.TrimRight(envString("CODEX_OAUTH_TOKEN_URL", defaultCodexOAuthToken), "/"),
		CodexOAuthRedirectURI: envString("CODEX_OAUTH_REDIRECT_URI", defaultOAuthRedirectURI),
		CodexOAuthScope:       envString("CODEX_OAUTH_SCOPE", defaultCodexOAuthScope),
	}
}

func (c Config) Validate() error {
	var problems []string

	if c.ListenAddr == "" {
		problems = append(problems, "GATEWAY_LISTEN_ADDR is empty")
	}
	if c.WSPath == "" || c.WSPath[0] != '/' {
		problems = append(problems, "GATEWAY_WS_PATH must start with '/'")
	}
	if c.OpusFrameDuration <= 0 {
		problems = append(problems, "GATEWAY_OPUS_FRAME_DURATION_MS must be > 0")
	}
	if c.SessionMaxTurn <= 0 {
		problems = append(problems, "GATEWAY_SESSION_MAX_TURN must be > 0")
	}
	if c.TTSMaxDuration <= 0 {
		problems = append(problems, "GATEWAY_TTS_MAX_DURATION must be > 0")
	}
	if c.STTStreamingEnabled && c.STTInterimInterval <= 0 {
		problems = append(problems, "GATEWAY_STT_INTERIM_INTERVAL must be > 0")
	}
	if c.STTStreamingEnabled && c.STTInterimMinAudio <= 0 {
		problems = append(problems, "GATEWAY_STT_INTERIM_MIN_AUDIO must be > 0")
	}
	if c.DownlinkSampleRate <= 0 {
		problems = append(problems, "GATEWAY_DOWNLINK_SAMPLE_RATE must be > 0")
	}
	if c.DownlinkOpusBitrate <= 0 {
		problems = append(problems, "GATEWAY_DOWNLINK_OPUS_BITRATE must be > 0")
	}
	if strings.TrimSpace(c.MemoryDir) == "" {
		problems = append(problems, "GATEWAY_MEMORY_DIR is empty")
	}
	if c.MemoryContextSize <= 0 {
		problems = append(problems, "GATEWAY_MEMORY_CONTEXT_SIZE must be > 0")
	}
	if (c.STTProvider == "openai" || c.TTSProvider == "openai") && c.OpenAIAPIKey == "" {
		problems = append(problems, "OPENAI_API_KEY is required for STT/TTS")
	}
	switch c.LLMProvider {
	case "codex":
		if c.CodexAuthFile == "" {
			problems = append(problems, "CODEX_AUTH_FILE is empty")
		} else if _, err := os.Stat(c.CodexAuthFile); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				problems = append(problems, fmt.Sprintf("CODEX_AUTH_FILE is not readable: %v", err))
			}
		}
		if strings.TrimSpace(c.CodexOAuthClient) == "" {
			problems = append(problems, "CODEX_OAUTH_CLIENT_ID is empty")
		}
		if !isValidURL(c.CodexOAuthAuthURL) {
			problems = append(problems, "CODEX_OAUTH_AUTHORIZE_URL is invalid")
		}
		if !isValidURL(c.CodexOAuthToken) {
			problems = append(problems, "CODEX_OAUTH_TOKEN_URL is invalid")
		}
		if !isValidURL(c.CodexOAuthRedirectURI) {
			problems = append(problems, "CODEX_OAUTH_REDIRECT_URI is invalid")
		}
		if strings.TrimSpace(c.CodexOAuthScope) == "" {
			problems = append(problems, "CODEX_OAUTH_SCOPE is empty")
		}
		if c.CodexMaxOutputTokens <= 0 {
			problems = append(problems, "CODEX_MAX_OUTPUT_TOKENS must be > 0")
		}
	case "openclaw":
		if strings.TrimSpace(c.OpenClawURL) == "" {
			problems = append(problems, "GATEWAY_OPENCLAW_URL is empty")
		}
		if c.OpenClawDialTimeout <= 0 {
			problems = append(problems, "GATEWAY_OPENCLAW_DIAL_TIMEOUT_SEC must be > 0")
		}
	default:
		problems = append(problems, fmt.Sprintf("unsupported GATEWAY_LLM_PROVIDER %q", c.LLMProvider))
	}
	switch c.TTSProvider {
	case "openai", "openclaw", "local":
	default:
		problems = append(problems, fmt.Sprintf("unsupported GATEWAY_TTS_PROVIDER %q", c.TTSProvider))
	}
	switch c.STTProvider {
	case "openai", "openclaw", "mlx-whisper", "local":
	default:
		problems = append(problems, fmt.Sprintf("unsupported GATEWAY_STT_PROVIDER %q", c.STTProvider))
	}
	if c.STTProvider == "mlx-whisper" || c.STTProvider == "local" {
		if strings.TrimSpace(c.MLXWhisperBin) == "" {
			problems = append(problems, "GATEWAY_MLX_WHISPER_BIN is empty")
		}
		if strings.TrimSpace(c.MLXWhisperModel) == "" {
			problems = append(problems, "GATEWAY_MLX_WHISPER_MODEL is empty")
		}
	}
	if c.STTProvider == "local" && strings.TrimSpace(c.LocalSTTAddr) == "" {
		problems = append(problems, "GATEWAY_LOCAL_STT_ADDR is empty")
	}
	if c.TTSProvider == "local" {
		if strings.TrimSpace(c.LocalTTSAddr) == "" {
			problems = append(problems, "GATEWAY_LOCAL_TTS_ADDR is empty")
		}
		if c.LocalTTSSampleRate <= 0 {
			problems = append(problems, "GATEWAY_LOCAL_TTS_SAMPLE_RATE must be > 0")
		}
		if c.LocalTTSRate <= 0 {
			problems = append(problems, "GATEWAY_LOCAL_TTS_RATE must be > 0")
		}
	}
	if c.LocalModuleStartupTimeout <= 0 {
		problems = append(problems, "GATEWAY_LOCAL_MODULE_STARTUP_TIMEOUT must be > 0")
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

func defaultAuthFile() string {
	if value := strings.TrimSpace(os.Getenv("CODEX_AUTH_FILE")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".opencode", "auth", "openai.json")
}

func defaultMemoryDir() string {
	if value := strings.TrimSpace(os.Getenv("GATEWAY_MEMORY_DIR")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "./data/memory"
	}
	return filepath.Join(home, ".gateway-memory")
}

func defaultInstructions(raw string) string {
	if raw != "" {
		return raw
	}
	return "You are Alfredo, a voice assistant running on an ESP32 robot. Reply conversationally, keep answers concise, and avoid markdown-heavy formatting because the response will be spoken aloud."
}

func normalizePath(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return defaultWSPath
	}
	if strings.HasPrefix(s, "/") {
		return s
	}
	return "/" + s
}

func normalizeLLMProvider(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "codex":
		return "codex"
	case "openclaw":
		return "openclaw"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func normalizeSpeechProvider(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "openai":
		return strings.ToLower(strings.TrimSpace(raw))
	case "openclaw":
		return "openclaw"
	case "local":
		return "local"
	case "mlx", "mlx-whisper", "whisper":
		return "mlx-whisper"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func envString(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func envBool(name string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	switch raw {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	case "":
		return fallback
	default:
		return fallback
	}
}

func envDuration(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	if parsed, err := time.ParseDuration(raw); err == nil {
		return parsed
	}
	if millis, err := strconv.Atoi(raw); err == nil {
		return time.Duration(millis) * time.Millisecond
	}
	return fallback
}

func envSeconds(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func discoverOpenClawConfig(path string) string {
	out, err := exec.Command("openclaw", "config", "get", path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func discoverOpenClawToken() string {
	token := strings.TrimSpace(discoverOpenClawConfig("gateway.auth.token"))
	if token != "" && !isRedactedSecret(token) {
		return token
	}

	path := discoverOpenClawConfigFile()
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return ""
		}
		path = filepath.Join(home, ".openclaw", "openclaw.json")
	}

	raw, err := os.ReadFile(expandUserPath(path))
	if err != nil {
		return ""
	}
	var parsed struct {
		Gateway struct {
			Auth struct {
				Token string `json:"token"`
			} `json:"auth"`
		} `json:"gateway"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	token = strings.TrimSpace(parsed.Gateway.Auth.Token)
	if isRedactedSecret(token) {
		return ""
	}
	return token
}

func discoverOpenClawConfigFile() string {
	out, err := exec.Command("openclaw", "config", "file").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func expandUserPath(path string) string {
	value := strings.TrimSpace(path)
	if value == "" {
		return ""
	}
	if value == "~" {
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			return home
		}
		return value
	}
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			return filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	return value
}

func isRedactedSecret(value string) bool {
	return strings.Trim(strings.TrimSpace(value), "\"") == "__OPENCLAW_REDACTED__"
}

func isValidURL(raw string) bool {
	value := strings.TrimSpace(raw)
	if value == "" {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	return parsed.Scheme != "" && parsed.Host != ""
}
