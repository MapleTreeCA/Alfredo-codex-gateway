package openclaw

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildVoiceModeMessage(t *testing.T) {
	t.Parallel()

	got := buildVoiceModeMessage("Turn on the light.")
	if !strings.Contains(got, "voice mode") {
		t.Fatalf("voice policy missing: %q", got)
	}
	if !strings.Contains(got, "User request:\nTurn on the light.") {
		t.Fatalf("user request missing: %q", got)
	}
}

func TestRemoveTextOnlyStyleLines(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		`Got it - I'm currently replying in text only.`,
		`If you want voice, say: "Use voice reply" and I'll speak.`,
		`Sure, I can set a timer for 10 minutes.`,
	}, "\n")

	got := removeTextOnlyStyleLines(input)
	want := "Sure, I can set a timer for 10 minutes."
	if got != want {
		t.Fatalf("unexpected cleaned output: got=%q want=%q", got, want)
	}
}

func TestCollectPayloadTextSkipsStatusOnlySummary(t *testing.T) {
	t.Parallel()

	payload := openClawAgentPayload{
		Status:  "ok",
		Summary: "completed",
	}

	got := collectPayloadText(payload)
	if got != "" {
		t.Fatalf("expected empty text for status-only payload, got %q", got)
	}
}

func TestCollectPayloadTextExtractsNestedTextPayload(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"content":[
			{"type":"text","text":"[[reply_to_current]] Hello from nested content."}
		]
	}`)
	payload := openClawAgentPayload{
		Status:  "ok",
		Summary: "completed",
	}
	payload.Result.Payloads = []json.RawMessage{raw}

	got := collectPayloadText(payload)
	want := "Hello from nested content."
	if got != want {
		t.Fatalf("unexpected extracted text: got=%q want=%q", got, want)
	}
}

func TestIsPlaceholderAssistantText(t *testing.T) {
	t.Parallel()

	cases := []string{
		"",
		"completed",
		"NO_REPLY",
		"[[audio_as_voice]]\nMEDIA:/tmp/voice.mp3",
		"MEDIA:/tmp/voice.mp3",
	}
	for _, item := range cases {
		if !isPlaceholderAssistantText(item) {
			t.Fatalf("expected placeholder text for %q", item)
		}
	}
	if isPlaceholderAssistantText("It is 10:11 AM.") {
		t.Fatalf("normal text should not be treated as placeholder")
	}
}

func TestSessionKeyFor(t *testing.T) {
	t.Parallel()

	client := &Client{defaultSessionKey: "alfredo"}
	if got := client.sessionKeyFor("session123"); got != "alfredo:session123" {
		t.Fatalf("unexpected session key: %q", got)
	}
	if got := (&Client{}).sessionKeyFor("session123"); got != "session123" {
		t.Fatalf("unexpected fallback session key: %q", got)
	}
}
