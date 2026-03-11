package localmodule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"gateway/internal/config"
	"gateway/internal/localtts"
	"gateway/internal/mlxwhisper"
)

const maxModuleAudioBytes = 20 << 20 // 20MB

type Manager struct {
	cfg config.Config

	mu        sync.Mutex
	sttServer *http.Server
	ttsServer *http.Server
}

func New(cfg config.Config) *Manager {
	return &Manager{cfg: cfg}
}

func (m *Manager) Ensure(ctx context.Context) error {
	if m.cfg.STTProvider == "local" {
		if err := m.ensureSTTModule(ctx); err != nil {
			return err
		}
	}
	if m.cfg.TTSProvider == "local" {
		if err := m.ensureTTSModule(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	stt := m.sttServer
	tts := m.ttsServer
	m.mu.Unlock()

	var errs []string
	if stt != nil {
		if err := stt.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("shutdown local stt module failed: %v", err))
		}
	}
	if tts != nil {
		if err := tts.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("shutdown local tts module failed: %v", err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (m *Manager) ensureSTTModule(ctx context.Context) error {
	if strings.TrimSpace(m.cfg.LocalSTTAddr) == "" {
		return errors.New("local stt module addr is empty")
	}
	if ok := checkHealth(ctx, "http://"+m.cfg.LocalSTTAddr+"/healthz"); ok {
		log.Printf("local stt module already running on %s", m.cfg.LocalSTTAddr)
		return nil
	}
	if _, err := exec.LookPath(strings.TrimSpace(m.cfg.MLXWhisperBin)); err != nil {
		return fmt.Errorf("local stt module requires %q binary: %w", m.cfg.MLXWhisperBin, err)
	}

	transcriber := mlxwhisper.NewTranscriber(m.cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/transcribe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxModuleAudioBytes)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read stt body failed"})
			return
		}
		if len(data) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "stt body is empty"})
			return
		}
		text, err := transcriber.Transcribe(r.Context(), data)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"text": text})
	})

	server := &http.Server{
		Addr:              m.cfg.LocalSTTAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := m.startModuleServer(ctx, "stt", server, m.cfg.LocalSTTAddr); err != nil {
		return err
	}
	m.mu.Lock()
	m.sttServer = server
	m.mu.Unlock()
	log.Printf("local stt module started on %s", m.cfg.LocalSTTAddr)
	return nil
}

func (m *Manager) ensureTTSModule(ctx context.Context) error {
	if strings.TrimSpace(m.cfg.LocalTTSAddr) == "" {
		return errors.New("local tts module addr is empty")
	}
	if ok := checkHealth(ctx, "http://"+m.cfg.LocalTTSAddr+"/healthz"); ok {
		log.Printf("local tts module already running on %s", m.cfg.LocalTTSAddr)
		return nil
	}
	if err := localtts.CheckDependencies(); err != nil {
		return fmt.Errorf("local tts module dependency check failed: %w", err)
	}

	synth := localtts.New(m.cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/synthesize", func(w http.ResponseWriter, r *http.Request) {
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
		wav, err := synth.SynthesizeWithOptions(r.Context(), req.Text, localtts.Options{
			Voice: req.Voice,
			Rate:  req.Rate,
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(wav)))
		_, _ = w.Write(wav)
	})

	server := &http.Server{
		Addr:              m.cfg.LocalTTSAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := m.startModuleServer(ctx, "tts", server, m.cfg.LocalTTSAddr); err != nil {
		return err
	}
	m.mu.Lock()
	m.ttsServer = server
	m.mu.Unlock()
	log.Printf("local tts module started on %s", m.cfg.LocalTTSAddr)
	return nil
}

func (m *Manager) startModuleServer(ctx context.Context, name string, server *http.Server, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen local %s module failed on %s: %w", name, addr, err)
	}
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("local %s module stopped: %v", name, err)
		}
	}()

	deadline := time.Now().Add(m.cfg.LocalModuleStartupTimeout)
	for time.Now().Before(deadline) {
		if checkHealth(ctx, "http://"+addr+"/healthz") {
			return nil
		}
		time.Sleep(120 * time.Millisecond)
	}
	_ = server.Shutdown(context.Background())
	return fmt.Errorf("local %s module startup timeout on %s", name, addr)
}

func checkHealth(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
