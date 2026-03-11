package memorystore

import (
	"strings"
	"testing"
	"time"
)

func TestRuntimeConfigRoundTrip(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}

	payload := []byte(`{"model":"gpt-5.4","online":false,"context_messages":17}`)
	if err := store.SaveRuntimeConfig(payload); err != nil {
		t.Fatalf("save runtime config failed: %v", err)
	}

	got, err := store.LoadRuntimeConfig()
	if err != nil {
		t.Fatalf("load runtime config failed: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("runtime config mismatch:\n got: %s\nwant: %s", string(got), string(payload))
	}
}

func TestRuntimeConfigEmptyWhenNotSet(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}

	got, err := store.LoadRuntimeConfig()
	if err != nil {
		t.Fatalf("load runtime config failed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty payload, got: %q", string(got))
	}
}

func TestSearchPageSinceFiltersByTime(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}

	if err := store.Append("sid-old", "user", "old kiwi note"); err != nil {
		t.Fatalf("append old failed: %v", err)
	}
	if err := store.Append("sid-new", "user", "fresh kiwi note"); err != nil {
		t.Fatalf("append new failed: %v", err)
	}

	oldCutoff := time.Now().AddDate(0, 0, -35).UnixMilli()
	if _, err := store.db.Exec(
		`UPDATE memory_entries SET created_at = ? WHERE session_id = ? AND LOWER(text) LIKE ?`,
		oldCutoff,
		"sid-old",
		"%old kiwi note%",
	); err != nil {
		t.Fatalf("update old timestamp failed: %v", err)
	}

	since := time.Now().AddDate(0, 0, -30).UnixMilli()
	items, total, err := store.SearchPageSince("", "kiwi", 20, 0, since)
	if err != nil {
		t.Fatalf("search since failed: %v", err)
	}
	if total != 1 {
		t.Fatalf("total=%d, want=1", total)
	}
	if len(items) != 1 {
		t.Fatalf("items len=%d, want=1", len(items))
	}
	if !strings.Contains(strings.ToLower(items[0].Text), "fresh kiwi note") {
		t.Fatalf("unexpected item text: %q", items[0].Text)
	}
}
