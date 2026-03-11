package memorystore

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultDBFileName = "memory.sqlite3"
	maxPageSize       = 1000
	runtimeConfigKey  = "runtime_config"
)

type Entry struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Text      string `json:"text"`
	CreatedAt int64  `json:"created_at"`
}

type SessionSummary struct {
	SessionID  string `json:"session_id"`
	EntryCount int    `json:"entry_count"`
	LastAt     int64  `json:"last_at"`
}

type Store struct {
	db     *sql.DB
	dbPath string
}

func New(root string) (*Store, error) {
	dbPath, err := resolveDBPath(root)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite failed: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{
		db:     db,
		dbPath: dbPath,
	}, nil
}

func (s *Store) DBPath() string {
	return s.dbPath
}

func (s *Store) Append(sessionID, role, text string) error {
	if s == nil || s.db == nil {
		return errors.New("memory store is not initialized")
	}
	sid := strings.TrimSpace(sessionID)
	r := strings.ToLower(strings.TrimSpace(role))
	value := strings.TrimSpace(text)
	if sid == "" || value == "" {
		return nil
	}
	if r != "user" && r != "assistant" {
		return nil
	}

	_, err := s.db.Exec(
		`INSERT INTO memory_entries(id, session_id, role, text, created_at) VALUES(?, ?, ?, ?, ?)`,
		randomID(),
		sid,
		r,
		value,
		time.Now().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("insert memory entry failed: %w", err)
	}
	return nil
}

func (s *Store) LoadRecent(sessionID string, limit int) ([]Entry, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("memory store is not initialized")
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" || limit <= 0 {
		return nil, nil
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}

	rows, err := s.db.Query(
		`SELECT id, session_id, role, text, created_at
		 FROM memory_entries
		 WHERE session_id = ?
		 ORDER BY created_at DESC
		 LIMIT ?`,
		sid,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent memory failed: %w", err)
	}
	defer rows.Close()

	desc := make([]Entry, 0, limit)
	for rows.Next() {
		var entry Entry
		if err := rows.Scan(&entry.ID, &entry.SessionID, &entry.Role, &entry.Text, &entry.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan recent memory failed: %w", err)
		}
		desc = append(desc, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent memory failed: %w", err)
	}

	// Keep chronological order for conversation context.
	for left, right := 0, len(desc)-1; left < right; left, right = left+1, right-1 {
		desc[left], desc[right] = desc[right], desc[left]
	}
	return desc, nil
}

func (s *Store) Search(sessionID, query string, limit int) ([]Entry, error) {
	items, _, err := s.SearchPage(sessionID, query, limit, 0)
	return items, err
}

func (s *Store) SearchPage(sessionID, query string, limit, offset int) ([]Entry, int, error) {
	return s.searchPage(sessionID, query, limit, offset, 0)
}

func (s *Store) SearchPageSince(
	sessionID, query string,
	limit, offset int,
	sinceUnixMilli int64,
) ([]Entry, int, error) {
	return s.searchPage(sessionID, query, limit, offset, sinceUnixMilli)
}

func (s *Store) searchPage(
	sessionID, query string,
	limit, offset int,
	sinceUnixMilli int64,
) ([]Entry, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, errors.New("memory store is not initialized")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if offset < 0 {
		offset = 0
	}

	whereSQL, whereArgs, scoreSQL, scoreArgs := buildSearchSQL(sessionID, query, sinceUnixMilli)

	var total int
	countQuery := `SELECT COUNT(1) FROM memory_entries WHERE ` + whereSQL
	if err := s.db.QueryRow(countQuery, whereArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count memory search failed: %w", err)
	}
	if total == 0 {
		return []Entry{}, 0, nil
	}

	queryArgs := append([]any(nil), whereArgs...)
	querySQL := `SELECT id, session_id, role, text, created_at
		FROM memory_entries
		WHERE ` + whereSQL
	if scoreSQL != "" {
		querySQL += ` ORDER BY (` + scoreSQL + `) DESC, created_at DESC`
		queryArgs = append(queryArgs, scoreArgs...)
	} else {
		querySQL += ` ORDER BY created_at DESC`
	}
	querySQL += ` LIMIT ? OFFSET ?`
	queryArgs = append(queryArgs, limit, offset)

	rows, err := s.db.Query(querySQL, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query memory search failed: %w", err)
	}
	defer rows.Close()

	items := make([]Entry, 0, limit)
	for rows.Next() {
		var entry Entry
		if err := rows.Scan(&entry.ID, &entry.SessionID, &entry.Role, &entry.Text, &entry.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan memory search failed: %w", err)
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate memory search failed: %w", err)
	}

	return items, total, nil
}

func (s *Store) ListSessions(limit int) ([]SessionSummary, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("memory store is not initialized")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}

	rows, err := s.db.Query(
		`SELECT session_id, COUNT(1) AS entry_count, MAX(created_at) AS last_at
		 FROM memory_entries
		 GROUP BY session_id
		 ORDER BY last_at DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query sessions failed: %w", err)
	}
	defer rows.Close()

	sessions := make([]SessionSummary, 0, limit)
	for rows.Next() {
		var item SessionSummary
		if err := rows.Scan(&item.SessionID, &item.EntryCount, &item.LastAt); err != nil {
			return nil, fmt.Errorf("scan sessions failed: %w", err)
		}
		sessions = append(sessions, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions failed: %w", err)
	}

	return sessions, nil
}

func (s *Store) LoadRuntimeConfig() ([]byte, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("memory store is not initialized")
	}

	var value string
	err := s.db.QueryRow(`SELECT value FROM runtime_settings WHERE key = ?`, runtimeConfigKey).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query runtime config failed: %w", err)
	}
	return []byte(value), nil
}

func (s *Store) SaveRuntimeConfig(payload []byte) error {
	if s == nil || s.db == nil {
		return errors.New("memory store is not initialized")
	}
	value := strings.TrimSpace(string(payload))
	if value == "" {
		return errors.New("runtime config payload is empty")
	}

	_, err := s.db.Exec(
		`INSERT INTO runtime_settings(key, value, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		runtimeConfigKey,
		value,
		time.Now().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("upsert runtime config failed: %w", err)
	}
	return nil
}

func resolveDBPath(root string) (string, error) {
	value := strings.TrimSpace(root)
	if value == "" {
		return "", errors.New("memory root dir is empty")
	}

	ext := strings.ToLower(filepath.Ext(value))
	switch ext {
	case ".db", ".sqlite", ".sqlite3":
		if err := os.MkdirAll(filepath.Dir(value), 0o755); err != nil {
			return "", fmt.Errorf("create db dir failed: %w", err)
		}
		return value, nil
	default:
		if err := os.MkdirAll(value, 0o755); err != nil {
			return "", fmt.Errorf("create memory dir failed: %w", err)
		}
		return filepath.Join(value, defaultDBFileName), nil
	}
}

func initSchema(db *sql.DB) error {
	schema := `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
CREATE TABLE IF NOT EXISTS memory_entries (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL,
	role TEXT NOT NULL,
	text TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memory_entries_session_created
	ON memory_entries(session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_memory_entries_created
	ON memory_entries(created_at DESC);
CREATE TABLE IF NOT EXISTS runtime_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);
	`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("init sqlite schema failed: %w", err)
	}
	return nil
}

func buildSearchSQL(
	sessionID, query string,
	sinceUnixMilli int64,
) (whereSQL string, whereArgs []any, scoreSQL string, scoreArgs []any) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 16)

	sid := strings.TrimSpace(sessionID)
	if sid != "" {
		conditions = append(conditions, "session_id = ?")
		args = append(args, sid)
	}
	if sinceUnixMilli > 0 {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, sinceUnixMilli)
	}

	tokens := splitTokens(query)
	if len(tokens) > 0 {
		tokenConditions := make([]string, 0, len(tokens))
		scoreParts := make([]string, 0, len(tokens))
		scoreValues := make([]any, 0, len(tokens))
		for _, token := range tokens {
			like := "%" + strings.ToLower(token) + "%"
			tokenConditions = append(tokenConditions, "LOWER(text) LIKE ?")
			args = append(args, like)

			scoreParts = append(scoreParts, "CASE WHEN INSTR(LOWER(text), ?) > 0 THEN 1 ELSE 0 END")
			scoreValues = append(scoreValues, strings.ToLower(token))
		}
		conditions = append(conditions, "("+strings.Join(tokenConditions, " OR ")+")")
		scoreSQL = strings.Join(scoreParts, " + ")
		scoreArgs = scoreValues
	}

	if len(conditions) == 0 {
		return "1=1", args, scoreSQL, scoreArgs
	}
	return strings.Join(conditions, " AND "), args, scoreSQL, scoreArgs
}

func splitTokens(value string) []string {
	raw := strings.Fields(strings.TrimSpace(strings.ToLower(value)))
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{})
	for _, token := range raw {
		t := strings.Trim(token, ".,!?;:\"'()[]{}<>")
		if len(t) < 2 {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func randomID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
