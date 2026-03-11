package server

import (
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultMemorySearchLimit   = 50
	defaultMemorySessionsLimit = 200
	maxMemoryResultLimit       = 500
	defaultMemoryPage          = 1
)

func (s *Server) HandleMemorySessionsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.memoryStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"sessions": []any{}, "count": 0})
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), defaultMemorySessionsLimit)
	sessions, err := s.memoryStore.ListSessions(limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": sessions,
		"count":    len(sessions),
	})
}

func (s *Server) HandleMemorySearchAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.memoryStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "count": 0})
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	pageSize := parseLimit(firstNonEmpty(r.URL.Query().Get("page_size"), r.URL.Query().Get("limit")), defaultMemorySearchLimit)
	page := parsePage(r.URL.Query().Get("page"))
	offset := (page - 1) * pageSize

	items, total, err := s.memoryStore.SearchPage(sessionID, query, pageSize, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"q":          query,
		"items":      items,
		"count":      len(items),
		"total":      total,
		"page":       page,
		"page_size":  pageSize,
		"has_more":   offset+len(items) < total,
	})
}

func (s *Server) HandleMemoryRecentAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.memoryStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "count": 0})
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id is required"})
		return
	}
	pageSize := parseLimit(firstNonEmpty(r.URL.Query().Get("page_size"), r.URL.Query().Get("limit")), defaultMemorySearchLimit)
	page := parsePage(r.URL.Query().Get("page"))
	offset := (page - 1) * pageSize
	items, total, err := s.memoryStore.SearchPage(sessionID, "", pageSize, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"items":      items,
		"count":      len(items),
		"total":      total,
		"page":       page,
		"page_size":  pageSize,
		"has_more":   offset+len(items) < total,
	})
}

func parseLimit(raw string, fallback int) int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return fallback
	}
	if n > maxMemoryResultLimit {
		return maxMemoryResultLimit
	}
	return n
}

func parsePage(raw string) int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return defaultMemoryPage
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return defaultMemoryPage
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
