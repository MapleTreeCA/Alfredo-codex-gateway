package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"gateway/internal/codexauth"
	"gateway/internal/config"
	"gateway/internal/local"
	"gateway/internal/localmodule"
	"gateway/internal/memorystore"
	"gateway/internal/mlxwhisper"
	"gateway/internal/openai"
	"gateway/internal/openclaw"
)

type llmClient interface {
	Generate(ctx context.Context, sessionID, userText string) (string, error)
}

type llmOptionClient interface {
	GenerateWithOptions(ctx context.Context, sessionID, userText string, options map[string]string) (string, error)
}

type llmDetailedOptionClient interface {
	GenerateWithOptionsDetailed(
		ctx context.Context,
		sessionID, userText string,
		options map[string]string,
	) (codexauth.GenerateDetailedResponse, error)
}

type transcriber interface {
	Transcribe(ctx context.Context, wav []byte) (string, error)
}

type speechSynthesizer interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

type speechOptionSynthesizer interface {
	SynthesizeWithOptions(ctx context.Context, text string, options map[string]string) ([]byte, error)
}

type Server struct {
	cfg          config.Config
	upgrader     websocket.Upgrader
	transcriber  transcriber
	sttCloser    io.Closer
	synthesizer  speechSynthesizer
	llm          llmClient
	localModule  *localmodule.Manager
	runtimeMu    sync.RWMutex
	runtime      runtimeConfig
	voiceCatalog voiceCatalog
	modelMu      sync.Mutex
	modelCatalog modelCatalog
	memoryStore  *memorystore.Store
	oauthMu      sync.Mutex
	oauthStates  map[string]oauthPendingState
	httpClient   *http.Client
	sessionsMu   sync.RWMutex
	sessions     map[string]*Session
}

type oauthPendingState struct {
	Verifier  string
	ExpiresAt time.Time
}

type connectedDevice struct {
	SessionID   string `json:"session_id"`
	DeviceID    string `json:"device_id"`
	ClientID    string `json:"client_id"`
	Protocol    string `json:"protocol"`
	RemoteAddr  string `json:"remote_addr"`
	ConnectedAt int64  `json:"connected_at"`
}

func New(cfg config.Config) (*Server, error) {
	memStore, err := memorystore.New(cfg.MemoryDir)
	if err != nil {
		return nil, fmt.Errorf("init memory store failed: %w", err)
	}
	runtime := defaultRuntimeConfig(cfg)
	if cfg.RuntimeConfigResetOnStart {
		if err := saveRuntimeConfigToStore(memStore, runtime); err != nil {
			log.Printf("reset runtime config into sqlite failed: %v", err)
		} else {
			log.Printf("runtime config reset on start enabled; persisted runtime replaced by defaults")
		}
	} else {
		persistedRuntime, found, err := loadRuntimeConfigFromStore(memStore, runtime)
		if err != nil {
			log.Printf("load runtime config from sqlite failed, fallback to defaults: %v", err)
		} else if found {
			runtime = persistedRuntime
		} else if err := saveRuntimeConfigToStore(memStore, runtime); err != nil {
			log.Printf("seed runtime config into sqlite failed: %v", err)
		}
	}
	log.Printf(
		"runtime config loaded model=%s effort=%s verbosity=%s online=%t concise=%t max_output_tokens=%d context_messages=%d memory_recall_days=%d tts_voice=%q tts_rate=%d session_silence_ms=%d session_max_turn_ms=%d stt_streaming_enabled=%t stt_interim_interval_ms=%d stt_interim_min_audio_ms=%d",
		runtime.Model,
		runtime.Effort,
		runtime.Verbosity,
		runtime.Online,
		runtime.Concise,
		runtime.MaxOutputTokens,
		runtime.ContextMessages,
		runtime.MemoryRecallDays,
		runtime.TTSVoice,
		runtime.TTSRate,
		runtime.SessionSilenceMS,
		runtime.SessionMaxTurnMS,
		runtime.STTStreamingEnabled,
		runtime.STTInterimIntervalMS,
		runtime.STTInterimMinAudioMS,
	)

	var client llmClient
	switch cfg.LLMProvider {
	case "openclaw":
		client = openclaw.New(cfg)
	default:
		client = codexauth.NewWithMemoryStore(cfg, memStore)
	}
	var stt transcriber
	switch cfg.STTProvider {
	case "openclaw":
		stt = openclaw.NewTranscriber(cfg)
	case "mlx-whisper":
		stt = mlxwhisper.NewTranscriber(cfg)
	case "local":
		stt = local.NewTranscriber(cfg)
	default:
		stt = openai.NewTranscriber(cfg)
	}
	sttCloser, _ := any(stt).(io.Closer)
	var synthesizer speechSynthesizer
	switch cfg.TTSProvider {
	case "openclaw":
		synthesizer = openclaw.NewSpeechSynthesizer(cfg)
	case "local":
		synthesizer = local.NewSpeechSynthesizer(cfg)
	default:
		synthesizer = openai.NewSpeechSynthesizer(cfg)
	}

	return &Server{
		cfg:          cfg,
		transcriber:  stt,
		sttCloser:    sttCloser,
		synthesizer:  synthesizer,
		llm:          client,
		localModule:  localmodule.New(cfg),
		runtime:      runtime,
		voiceCatalog: loadVoiceCatalog(),
		memoryStore:  memStore,
		oauthStates:  make(map[string]oauthPendingState),
		sessions:     make(map[string]*Session),
		httpClient: &http.Client{
			Timeout: 25 * time.Second,
		},
		upgrader: websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool { return true },
		},
	}, nil
}

func (s *Server) registerSession(session *Session) {
	if session == nil {
		return
	}
	s.sessionsMu.Lock()
	s.sessions[session.id] = session
	s.sessionsMu.Unlock()
}

func (s *Server) unregisterSession(sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	s.sessionsMu.Lock()
	delete(s.sessions, sessionID)
	s.sessionsMu.Unlock()
}

func (s *Server) listConnectedDevices() []connectedDevice {
	s.sessionsMu.RLock()
	devices := make([]connectedDevice, 0, len(s.sessions))
	for _, session := range s.sessions {
		if session == nil {
			continue
		}
		devices = append(devices, session.snapshotDevice())
	}
	s.sessionsMu.RUnlock()

	sort.Slice(devices, func(i, j int) bool {
		return devices[i].ConnectedAt > devices[j].ConnectedAt
	})
	return devices
}

func (s *Server) sendSystemCommandToDevice(
	sessionID string,
	command string,
	config map[string]any,
	reboot bool,
) error {
	s.sessionsMu.RLock()
	session := s.sessions[sessionID]
	s.sessionsMu.RUnlock()
	if session == nil {
		return fmt.Errorf("device session not connected: %s", sessionID)
	}
	if err := session.sendSystemCommand(command, config, reboot); err != nil {
		return fmt.Errorf("send system command failed: %w", err)
	}
	return nil
}

func (s *Server) Prepare(ctx context.Context) error {
	return s.localModule.Ensure(ctx)
}

func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade failed: %v", err)
		return
	}

	session := newSession(s, conn, r)
	session.run()
}

func (s *Server) HandleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) authorized(r *http.Request) bool {
	if s.cfg.WSToken == "" {
		return true
	}
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		return false
	}
	if strings.EqualFold(raw, "Bearer "+s.cfg.WSToken) {
		return true
	}
	return raw == s.cfg.WSToken
}

func (s *Server) Shutdown(ctx context.Context) error {
	var errs []string
	if s.sttCloser != nil {
		if err := s.sttCloser.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("close stt transcriber failed: %v", err))
		}
	}
	if s.localModule != nil {
		if err := s.localModule.Shutdown(ctx); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
