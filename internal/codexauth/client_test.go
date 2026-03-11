package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"gateway/internal/memorystore"
)

func TestNormalizeModel(t *testing.T) {
	tests := map[string]string{
		"":                    "gpt-5.1-codex",
		"gpt-5.2-codex-high":  "gpt-5.2-codex",
		"gpt-5.1-codex-mini":  "gpt-5.1-codex-mini",
		"gpt-5-codex":         "gpt-5.1-codex",
		"gpt-5-medium":        "gpt-5.1",
		"random-custom-model": "gpt-5.1-codex",
	}

	for input, want := range tests {
		if got := normalizeModel(input); got != want {
			t.Fatalf("normalizeModel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExtractAccountID(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payloadBody, err := json.Marshal(map[string]any{
		jwtClaims: map[string]any{
			"chatgpt_account_id": "acct_123",
		},
	})
	if err != nil {
		t.Fatalf("marshal payload failed: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBody)
	token := header + "." + payload + ".signature"

	accountID, err := extractAccountID(token)
	if err != nil {
		t.Fatalf("extractAccountID failed: %v", err)
	}
	if accountID != "acct_123" {
		t.Fatalf("unexpected account ID %q", accountID)
	}
}

func TestComposeInstructions(t *testing.T) {
	got := composeInstructions("You are Alfredo.", "gpt-5.1-codex")
	if !strings.Contains(got, "You are Alfredo.") {
		t.Fatalf("missing base instructions: %q", got)
	}
	if !strings.Contains(got, "Runtime model id: gpt-5.1-codex") {
		t.Fatalf("missing runtime model id: %q", got)
	}
}

func TestComposeInstructionsWithConciseRule(t *testing.T) {
	base := "You are Alfredo."
	got := composeInstructionsWithConciseRule(base)
	if !strings.Contains(got, "Be concise.") {
		t.Fatalf("missing concise rule: %q", got)
	}

	again := composeInstructionsWithConciseRule(got)
	if strings.Count(again, "Be concise.") != 1 {
		t.Fatalf("concise rule duplicated: %q", again)
	}
}

func TestComposeInstructionsWithConciseRuleOption(t *testing.T) {
	base := "You are Alfredo."
	enabled := composeInstructionsWithConciseRuleOption(base, true, true)
	if !strings.Contains(enabled, "Be concise.") {
		t.Fatalf("expected concise rule when enabled: %q", enabled)
	}

	disabled := composeInstructionsWithConciseRuleOption(base, false, true)
	if strings.Contains(strings.ToLower(disabled), "be concise") {
		t.Fatalf("did not expect concise rule when disabled: %q", disabled)
	}

	defaulted := composeInstructionsWithConciseRuleOption(base, false, false)
	if !strings.Contains(defaulted, "Be concise.") {
		t.Fatalf("expected default concise rule when not overridden: %q", defaulted)
	}
}

func TestResolveMaxOutputTokens(t *testing.T) {
	if got := resolveMaxOutputTokens(0); got != 500 {
		t.Fatalf("resolveMaxOutputTokens(0) = %d, want 500", got)
	}
	if got := resolveMaxOutputTokens(256); got != 256 {
		t.Fatalf("resolveMaxOutputTokens(256) = %d, want 256", got)
	}
}

func TestNormalizeOnlineFlag(t *testing.T) {
	tests := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"true":  true,
		"1":     true,
		"yes":   true,
		"on":    true,
	}
	for input, want := range tests {
		if got := normalizeOnlineFlag(input); got != want {
			t.Fatalf("normalizeOnlineFlag(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestBuildInputMessagesIncludesHistory(t *testing.T) {
	client := &Client{
		maxHistory: defaultHistoryLimit,
		history: map[string][]conversationMessage{
			"sid": {
				{Role: "user", Text: "first question"},
				{Role: "assistant", Text: "first answer"},
			},
		},
	}

	input, memoryMessages := client.buildInputMessages("sid", "second question", 6)
	if len(input) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(input))
	}
	if memoryMessages != 2 {
		t.Fatalf("memory message count=%d, want=2", memoryMessages)
	}

	limited, limitedMemory := client.buildInputMessages("sid", "third question", 1)
	if len(limited) != 2 {
		t.Fatalf("expected 2 messages with limit=1, got %d", len(limited))
	}
	if limitedMemory != 1 {
		t.Fatalf("limited memory count=%d, want=1", limitedMemory)
	}
}

func TestAppendHistoryTrims(t *testing.T) {
	client := &Client{
		maxHistory: 4,
		history:    map[string][]conversationMessage{},
	}
	for i := 0; i < 6; i++ {
		if err := client.appendHistory("sid", "u", "a"); err != nil {
			t.Fatalf("appendHistory failed: %v", err)
		}
	}
	if got := len(client.history["sid"]); got != 4 {
		t.Fatalf("history length=%d, want=%d", got, 4)
	}
}

func TestLoadHistoryFromPersistentStore(t *testing.T) {
	dir := t.TempDir()
	store, err := memorystore.New(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatalf("new memorystore failed: %v", err)
	}
	if err := store.Append("sid", "user", "who am i?"); err != nil {
		t.Fatalf("append user failed: %v", err)
	}
	if err := store.Append("sid", "assistant", "you are alfredo"); err != nil {
		t.Fatalf("append assistant failed: %v", err)
	}

	client := &Client{
		maxHistory:  8,
		memoryStore: store,
		history:     map[string][]conversationMessage{},
	}
	input, memoryMessages := client.buildInputMessages("sid", "remember me", 8)
	if len(input) != 3 {
		t.Fatalf("expected 3 input messages, got %d", len(input))
	}
	if memoryMessages != 2 {
		t.Fatalf("memory message count=%d, want=2", memoryMessages)
	}
}

func TestParseContextMessages(t *testing.T) {
	tests := map[string]int{
		"":    0,
		"0":   0,
		"-1":  0,
		"abc": 0,
		"8":   8,
	}
	for input, want := range tests {
		if got := parseContextMessages(input); got != want {
			t.Fatalf("parseContextMessages(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestParseMemoryRecallDays(t *testing.T) {
	tests := map[string]int{
		"":    0,
		"0":   0,
		"-1":  0,
		"abc": 0,
		"30":  30,
	}
	for input, want := range tests {
		if got := parseMemoryRecallDays(input); got != want {
			t.Fatalf("parseMemoryRecallDays(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestParseMaxOutputTokens(t *testing.T) {
	tests := map[string]int{
		"":     0,
		"0":    0,
		"-10":  0,
		"abc":  0,
		"500":  500,
		"2048": 2048,
	}
	for input, want := range tests {
		if got := parseMaxOutputTokens(input); got != want {
			t.Fatalf("parseMaxOutputTokens(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestParseOptionalBool(t *testing.T) {
	truthy := []string{"1", "true", "yes", "on"}
	for _, input := range truthy {
		got, ok := parseOptionalBool(input)
		if !ok || !got {
			t.Fatalf("parseOptionalBool(%q) = (%v,%v), want (true,true)", input, got, ok)
		}
	}
	falsy := []string{"0", "false", "no", "off"}
	for _, input := range falsy {
		got, ok := parseOptionalBool(input)
		if !ok || got {
			t.Fatalf("parseOptionalBool(%q) = (%v,%v), want (false,true)", input, got, ok)
		}
	}
	got, ok := parseOptionalBool("maybe")
	if ok || got {
		t.Fatalf("parseOptionalBool(%q) = (%v,%v), want (false,false)", "maybe", got, ok)
	}
}

func TestExtractUsage(t *testing.T) {
	input := map[string]any{
		"response": map[string]any{
			"usage": map[string]any{
				"input_tokens":  123.0,
				"output_tokens": 45.0,
				"total_tokens":  168.0,
			},
		},
	}
	usage := extractUsage(input)
	if usage.InputTokens != 123 || usage.OutputTokens != 45 || usage.TotalTokens != 168 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestComposeInstructionsWithMemoryHints(t *testing.T) {
	base := "Base instructions.\n\nRuntime model id: gpt-5.1-codex."
	hints := "Relevant memory hints from the last 30 days:\n- user likes tea"
	got := composeInstructionsWithMemoryHints(base, hints)
	if !strings.Contains(got, "Base instructions.") {
		t.Fatalf("missing base instructions: %q", got)
	}
	if !strings.Contains(got, "Relevant memory hints from the last 30 days") {
		t.Fatalf("missing memory hints: %q", got)
	}
}

func TestBuildRelevantMemoryHintsLimitedAndShort(t *testing.T) {
	dir := t.TempDir()
	store, err := memorystore.New(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatalf("new memorystore failed: %v", err)
	}
	for i := 0; i < 8; i++ {
		text := "kiwi preference note " + strings.Repeat("very-long-", 40)
		if err := store.Append("sid-global", "user", text); err != nil {
			t.Fatalf("append memory %d failed: %v", i, err)
		}
	}

	client := &Client{
		maxHistory:  10,
		memoryStore: store,
		history: map[string][]conversationMessage{
			"sid-current": {
				{Role: "user", Text: "already in context"},
			},
		},
	}
	history := client.loadHistory("sid-current", 10)
	hints, count := client.buildRelevantMemoryHints("kiwi", history, 10, 30)
	if count <= 0 {
		t.Fatalf("expected at least one hint, got count=%d hints=%q", count, hints)
	}
	if count > client.resolveRecallItemLimit(10) {
		t.Fatalf("hint count=%d exceeds recall limit=%d", count, client.resolveRecallItemLimit(10))
	}
	if utf8Len(hints) > defaultRecallTotalMax+200 {
		t.Fatalf("hints are too long: len=%d hints=%q", utf8Len(hints), hints)
	}
	if !strings.Contains(hints, "last 30 days") {
		t.Fatalf("missing time window in hints: %q", hints)
	}
}

func TestBuildRelevantMemoryHintsDetailedStats(t *testing.T) {
	dir := t.TempDir()
	store, err := memorystore.New(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatalf("new memorystore failed: %v", err)
	}
	if err := store.Append("sid-a", "user", "kiwi note from session A "+strings.Repeat("x", 200)); err != nil {
		t.Fatalf("append sid-a failed: %v", err)
	}
	if err := store.Append("sid-b", "assistant", "kiwi note from session B "+strings.Repeat("y", 200)); err != nil {
		t.Fatalf("append sid-b failed: %v", err)
	}

	client := &Client{
		maxHistory:  10,
		memoryStore: store,
		history:     map[string][]conversationMessage{},
	}

	result := client.buildRelevantMemoryHintsDetailed("kiwi", nil, 10, 30)
	if result.Stats.Candidates < 2 {
		t.Fatalf("expected >=2 candidates, got %d", result.Stats.Candidates)
	}
	if result.Stats.InjectedCount <= 0 {
		t.Fatalf("expected injected hints, got %d", result.Stats.InjectedCount)
	}
	if result.Stats.InjectedRawRunes <= 0 || result.Stats.InjectedSnippetRunes <= 0 || result.Stats.InjectedHintRunes <= 0 {
		t.Fatalf("invalid rune stats: %+v", result.Stats)
	}
	if result.Stats.InjectedRawRunes < result.Stats.InjectedSnippetRunes {
		t.Fatalf("raw runes should be >= snippet runes: %+v", result.Stats)
	}
	gotSessions := strings.Join(result.Stats.SourceSessions, ",")
	if !strings.Contains(gotSessions, "sid-a") || !strings.Contains(gotSessions, "sid-b") {
		t.Fatalf("missing source sessions: %v", result.Stats.SourceSessions)
	}
	if strings.TrimSpace(result.Hints) == "" {
		t.Fatalf("expected non-empty hints")
	}
}
