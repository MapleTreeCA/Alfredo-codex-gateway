package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gateway/internal/config"
	"gateway/internal/server"
)

func main() {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid gateway config: %v", err)
	}

	gateway, err := server.New(cfg)
	if err != nil {
		log.Fatalf("create gateway failed: %v", err)
	}
	startupCtx, startupCancel := context.WithTimeout(context.Background(), cfg.LocalModuleStartupTimeout+5*time.Second)
	if err := gateway.Prepare(startupCtx); err != nil {
		startupCancel()
		log.Fatalf("prepare local modules failed: %v", err)
	}
	startupCancel()

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.WSPath, gateway.HandleWS)
	mux.HandleFunc("/healthz", gateway.HandleHealth)
	mux.HandleFunc("/oauth2/initiate", gateway.HandleOAuthInitiate)
	mux.HandleFunc("/oauth2/callback", gateway.HandleOAuthCallback)
	mux.HandleFunc("/auth/callback", gateway.HandleOAuthCallback)
	mux.HandleFunc("/api/oauth/status", gateway.HandleOAuthStatus)
	mux.HandleFunc("/api/runtime/config", gateway.HandleRuntimeConfigAPI)
	mux.HandleFunc("/api/chat", gateway.HandleChatAPI)
	mux.HandleFunc("/api/memory/sessions", gateway.HandleMemorySessionsAPI)
	mux.HandleFunc("/api/memory/search", gateway.HandleMemorySearchAPI)
	mux.HandleFunc("/api/memory/recent", gateway.HandleMemoryRecentAPI)
	mux.HandleFunc("/api/devices", gateway.HandleConnectedDevicesAPI)
	mux.HandleFunc("/api/devices/sdcard-config", gateway.HandleDeviceSDConfigAPI)
	mux.HandleFunc("/api/transcribe", gateway.HandleTranscribeAPI)
	mux.HandleFunc("/api/tts", gateway.HandleSynthesizeAPI)
	mux.HandleFunc("/", gateway.HandleWeb)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		_ = gateway.Shutdown(shutdownCtx)
	}()

	log.Printf(
		"gateway listening on %s%s llm_provider=%s stt_provider=%s tts_provider=%s tts_max_duration=%s ui=http://127.0.0.1%s/",
		cfg.ListenAddr,
		cfg.WSPath,
		cfg.LLMProvider,
		cfg.STTProvider,
		cfg.TTSProvider,
		cfg.TTSMaxDuration.Round(time.Millisecond),
		cfg.ListenAddr,
	)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("gateway server failed: %v", err)
	}
}
