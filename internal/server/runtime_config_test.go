package server

import (
	"encoding/json"
	"testing"

	"gateway/internal/memorystore"
)

func TestLoadRuntimeConfigFromStoreMergesWithFallback(t *testing.T) {
	store, err := memorystore.New(t.TempDir())
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}

	if err := store.SaveRuntimeConfig([]byte(`{"model":"gpt-5.4","online":false,"concise":false,"max_output_tokens":640,"context_messages":17,"memory_recall_days":21}`)); err != nil {
		t.Fatalf("seed runtime config failed: %v", err)
	}

	fallback := runtimeConfig{
		Model:                "gpt-5.1-codex",
		Effort:               "medium",
		Verbosity:            "medium",
		Online:               true,
		Concise:              true,
		MaxOutputTokens:      1000,
		ContextMessages:      10,
		MemoryRecallDays:     30,
		TTSVoice:             "Daniel",
		TTSRate:              220,
		SessionSilenceMS:     1500,
		SessionMaxTurnMS:     18000,
		STTStreamingEnabled:  true,
		STTInterimIntervalMS: 1200,
		STTInterimMinAudioMS: 1600,
	}

	got, found, err := loadRuntimeConfigFromStore(store, fallback)
	if err != nil {
		t.Fatalf("load runtime config failed: %v", err)
	}
	if !found {
		t.Fatalf("expected persisted config to be found")
	}
	if got.Model != "gpt-5.4" {
		t.Fatalf("model mismatch: got=%q want=%q", got.Model, "gpt-5.4")
	}
	if got.Online {
		t.Fatalf("online mismatch: got=true want=false")
	}
	if got.ContextMessages != 17 {
		t.Fatalf("context_messages mismatch: got=%d want=%d", got.ContextMessages, 17)
	}
	if got.MemoryRecallDays != 21 {
		t.Fatalf("memory_recall_days mismatch: got=%d want=%d", got.MemoryRecallDays, 21)
	}
	if got.Concise {
		t.Fatalf("concise mismatch: got=true want=false")
	}
	if got.MaxOutputTokens != 640 {
		t.Fatalf("max_output_tokens mismatch: got=%d want=%d", got.MaxOutputTokens, 640)
	}
	if got.Effort != "medium" || got.Verbosity != "medium" {
		t.Fatalf("fallback effort/verbosity mismatch: got effort=%q verbosity=%q", got.Effort, got.Verbosity)
	}
	if got.TTSVoice != "Daniel" || got.TTSRate != 220 {
		t.Fatalf("fallback tts mismatch: got voice=%q rate=%d", got.TTSVoice, got.TTSRate)
	}
	if got.SessionSilenceMS != 1500 || got.SessionMaxTurnMS != 18000 {
		t.Fatalf("fallback session timing mismatch: %+v", got)
	}
	if !got.STTStreamingEnabled || got.STTInterimIntervalMS != 1200 || got.STTInterimMinAudioMS != 1600 {
		t.Fatalf("fallback stt timing mismatch: %+v", got)
	}
}

func TestPatchRuntimeConfigPersistsToSQLite(t *testing.T) {
	store, err := memorystore.New(t.TempDir())
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}

	s := &Server{
		memoryStore: store,
		runtime: runtimeConfig{
			Model:                "gpt-5.1-codex",
			Effort:               "medium",
			Verbosity:            "medium",
			Online:               true,
			Concise:              true,
			MaxOutputTokens:      1000,
			ContextMessages:      10,
			MemoryRecallDays:     30,
			TTSVoice:             "Daniel",
			TTSRate:              220,
			SessionSilenceMS:     1200,
			SessionMaxTurnMS:     15000,
			STTStreamingEnabled:  true,
			STTInterimIntervalMS: 900,
			STTInterimMinAudioMS: 1200,
		},
	}

	model := "gpt-5.3"
	effort := "high"
	online := false
	concise := false
	maxOutputTokens := 700
	contextMessages := 25
	memoryRecallDays := 45
	voice := "Reed"
	rate := 280
	sessionSilenceMS := 900
	sessionMaxTurnMS := 28000
	sttStreamingEnabled := false
	sttInterimIntervalMS := 650
	sttInterimMinAudioMS := 1000
	next, err := s.patchRuntimeConfig(runtimeConfigPatchRequest{
		Model:                &model,
		Effort:               &effort,
		Online:               &online,
		Concise:              &concise,
		MaxOutputTokens:      &maxOutputTokens,
		ContextMessages:      &contextMessages,
		MemoryRecallDays:     &memoryRecallDays,
		TTSVoice:             &voice,
		TTSRate:              &rate,
		SessionSilenceMS:     &sessionSilenceMS,
		SessionMaxTurnMS:     &sessionMaxTurnMS,
		STTStreamingEnabled:  &sttStreamingEnabled,
		STTInterimIntervalMS: &sttInterimIntervalMS,
		STTInterimMinAudioMS: &sttInterimMinAudioMS,
	})
	if err != nil {
		t.Fatalf("patch runtime config failed: %v", err)
	}
	if next.Model != "gpt-5.3" || next.Effort != "high" || next.Online {
		t.Fatalf("runtime patch mismatch: %+v", next)
	}
	if next.Concise || next.MaxOutputTokens != 700 {
		t.Fatalf("runtime patch concise/max_output mismatch: %+v", next)
	}
	if next.ContextMessages != 25 || next.TTSVoice != "Reed" || next.TTSRate != 280 {
		t.Fatalf("runtime patch mismatch: %+v", next)
	}
	if next.MemoryRecallDays != 45 {
		t.Fatalf("runtime patch mismatch: %+v", next)
	}
	if next.SessionSilenceMS != 900 || next.SessionMaxTurnMS != 28000 {
		t.Fatalf("runtime patch timing mismatch: %+v", next)
	}
	if next.STTStreamingEnabled || next.STTInterimIntervalMS != 650 || next.STTInterimMinAudioMS != 1000 {
		t.Fatalf("runtime patch stt mismatch: %+v", next)
	}

	raw, err := store.LoadRuntimeConfig()
	if err != nil {
		t.Fatalf("load runtime config payload failed: %v", err)
	}
	var persisted runtimeConfig
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decode persisted payload failed: %v", err)
	}
	if persisted.Model != "gpt-5.3" || persisted.Effort != "high" || persisted.Online {
		t.Fatalf("persisted runtime mismatch: %+v", persisted)
	}
	if persisted.Concise || persisted.MaxOutputTokens != 700 {
		t.Fatalf("persisted runtime concise/max_output mismatch: %+v", persisted)
	}
	if persisted.ContextMessages != 25 || persisted.TTSVoice != "Reed" || persisted.TTSRate != 280 {
		t.Fatalf("persisted runtime mismatch: %+v", persisted)
	}
	if persisted.MemoryRecallDays != 45 {
		t.Fatalf("persisted runtime mismatch: %+v", persisted)
	}
	if persisted.SessionSilenceMS != 900 || persisted.SessionMaxTurnMS != 28000 {
		t.Fatalf("persisted runtime timing mismatch: %+v", persisted)
	}
	if persisted.STTStreamingEnabled || persisted.STTInterimIntervalMS != 650 || persisted.STTInterimMinAudioMS != 1000 {
		t.Fatalf("persisted runtime stt mismatch: %+v", persisted)
	}
}

func TestSanitizeRuntimeConfigClampsVerbosityForGPT52Codex(t *testing.T) {
	got := sanitizeRuntimeConfig(runtimeConfig{
		Model:     "gpt-5.2-codex",
		Effort:    "medium",
		Verbosity: "low",
	})
	if got.Verbosity != "medium" {
		t.Fatalf("verbosity = %q, want %q", got.Verbosity, "medium")
	}
}
