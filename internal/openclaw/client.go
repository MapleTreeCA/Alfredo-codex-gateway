package openclaw

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"gateway/internal/config"

	"github.com/gorilla/websocket"
)

const (
	protocolVersion = 3
	clientID        = "gateway-client"
	clientMode      = "backend"
	clientRole      = "operator"
)

const voiceReplyPolicyPrompt = `System policy for this channel:
- You are in voice mode.
- Reply directly as a spoken assistant.
- Never say you are "replying in text only".
- Never ask the user to say "Use voice reply".
- Never call the "tts" tool.
- Never output "NO_REPLY".
- Always return a plain text assistant reply in this turn.`

var textOnlyStylePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\breplying in text only\b`),
	regexp.MustCompile(`(?i)\btext[- ]only\b`),
	regexp.MustCompile(`(?i)\bif you want voice\b`),
	regexp.MustCompile(`(?i)\buse voice reply\b`),
	regexp.MustCompile(`(?i)\bvoice reply\b`),
}

var multiSpaceRE = regexp.MustCompile(`[ \t\f\v]+`)
var contractionRE = regexp.MustCompile(`([A-Za-z])\s*'\s*([A-Za-z])`)
var spaceBeforePunctRE = regexp.MustCompile(`\s+([,.;:!?，。！？；：])`)

type Client struct {
	url               string
	token             string
	defaultSessionKey string
	defaultAgentID    string
	dialTimeout       time.Duration
}

func New(cfg config.Config) *Client {
	return &Client{
		url:               strings.TrimSpace(cfg.OpenClawURL),
		token:             strings.TrimSpace(cfg.OpenClawToken),
		defaultSessionKey: strings.TrimSpace(cfg.OpenClawSessionKey),
		defaultAgentID:    strings.TrimSpace(cfg.OpenClawAgentID),
		dialTimeout:       cfg.OpenClawDialTimeout,
	}
}

func (c *Client) Prime(ctx context.Context, sessionID string) error {
	_ = sessionID

	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	return c.connectGateway(ctx, conn)
}

func (c *Client) Generate(ctx context.Context, sessionID, userText string) (string, error) {
	sessionKey := c.sessionKeyFor(sessionID)
	idempotencyKey := randomID("alfredo-turn")

	text, _, err := c.replyOnce(ctx, sessionKey, strings.TrimSpace(userText), idempotencyKey)
	if err != nil && isGatewayRestartError(err) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(350 * time.Millisecond):
		}
		text, _, err = c.replyOnce(ctx, sessionKey, strings.TrimSpace(userText), idempotencyKey)
	}
	if err != nil {
		return "", err
	}

	text = normalizeModelText(removeTextOnlyStyleLines(text))
	if isPlaceholderAssistantText(text) {
		text = ""
	}
	if text == "" {
		text = "I am here. Please continue with your request."
	}
	return text, nil
}

func (c *Client) replyOnce(ctx context.Context, sessionKey, userText, idempotencyKey string) (string, string, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return "", "", err
	}
	defer conn.Close()

	if err := c.connectGateway(ctx, conn); err != nil {
		return "", "", err
	}
	return c.callAgent(ctx, conn, sessionKey, userText, idempotencyKey)
}

func (c *Client) dial(ctx context.Context) (*websocket.Conn, error) {
	if c.url == "" {
		return nil, errors.New("GATEWAY_OPENCLAW_URL is empty")
	}
	dialCtx, cancel := context.WithTimeout(ctx, c.dialTimeout)
	defer cancel()

	headers := http.Header{}
	if c.token != "" {
		headers.Set("Authorization", "Bearer "+c.token)
	}

	dialer := websocket.Dialer{HandshakeTimeout: c.dialTimeout}
	conn, _, err := dialer.DialContext(dialCtx, c.url, headers)
	if err != nil {
		return nil, fmt.Errorf("openclaw dial failed: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return conn, nil
}

func (c *Client) connectGateway(ctx context.Context, conn *websocket.Conn) error {
	identity, err := loadOrCreateDeviceIdentity()
	if err != nil {
		return fmt.Errorf("openclaw device identity failed: %w", err)
	}

	nonce, err := readConnectChallengeNonce(ctx, conn)
	if err != nil {
		return fmt.Errorf("openclaw challenge read failed: %w", err)
	}

	reqID := randomID("connect")
	scopes := []string{"operator.write"}
	signedAtMs := time.Now().UnixMilli()
	platform := resolveClientPlatform()
	authPayload := buildDeviceAuthPayloadV3(deviceAuthPayloadParams{
		deviceID:   identity.DeviceID,
		clientID:   clientID,
		clientMode: clientMode,
		role:       clientRole,
		scopes:     scopes,
		signedAt:   signedAtMs,
		token:      c.token,
		nonce:      nonce,
		platform:   platform,
	})
	signature := base64.RawURLEncoding.EncodeToString(
		ed25519.Sign(identity.PrivateKey, []byte(authPayload)),
	)

	params := map[string]any{
		"minProtocol": protocolVersion,
		"maxProtocol": protocolVersion,
		"client": map[string]any{
			"id":         clientID,
			"version":    "alfredo-gateway/0.1.0",
			"platform":   platform,
			"mode":       clientMode,
			"instanceId": identity.DeviceID,
		},
		"caps":   []string{},
		"role":   clientRole,
		"scopes": scopes,
		"device": map[string]any{
			"id":        identity.DeviceID,
			"publicKey": identity.PublicKeyRawBase64URL,
			"signature": signature,
			"signedAt":  signedAtMs,
			"nonce":     nonce,
		},
	}
	if c.token != "" {
		params["auth"] = map[string]string{"token": c.token}
	}
	debugJSON("openclaw connect params", params)

	if err := conn.WriteJSON(gatewayReqFrame{
		Type:   "req",
		ID:     reqID,
		Method: "connect",
		Params: params,
	}); err != nil {
		return fmt.Errorf("openclaw connect write failed: %w", err)
	}

	for {
		frame, err := readFrame(ctx, conn)
		if err != nil {
			return fmt.Errorf("openclaw connect read failed: %w", err)
		}
		if frame.Type != "res" || frame.ID != reqID {
			continue
		}
		if !frame.OK {
			return fmt.Errorf("openclaw connect rejected: %s", frame.errorMessage())
		}
		return nil
	}
}

func (c *Client) callAgent(
	ctx context.Context,
	conn *websocket.Conn,
	sessionKey string,
	userText string,
	idempotencyKey string,
) (string, string, error) {
	if userText == "" {
		return "", "", errors.New("empty user text")
	}

	reqID := randomID("agent")
	params := map[string]any{
		"message":        buildVoiceModeMessage(userText),
		"deliver":        false,
		"idempotencyKey": idempotencyKey,
		"sessionKey":     sessionKey,
	}
	if c.defaultAgentID != "" {
		params["agentId"] = c.defaultAgentID
	}
	debugJSON("openclaw agent params", params)

	if err := conn.WriteJSON(gatewayReqFrame{
		Type:   "req",
		ID:     reqID,
		Method: "agent",
		Params: params,
	}); err != nil {
		return "", "", fmt.Errorf("openclaw agent write failed: %w", err)
	}

	for {
		frame, err := readFrame(ctx, conn)
		if err != nil {
			return "", "", fmt.Errorf("openclaw agent read failed: %w", err)
		}
		if frame.Type != "res" || frame.ID != reqID {
			continue
		}
		if !frame.OK {
			return "", "", fmt.Errorf("openclaw agent failed: %s", frame.errorMessage())
		}

		var payload openClawAgentPayload
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return "", "", fmt.Errorf("openclaw payload parse failed: %w", err)
		}
		if payload.Status == "accepted" || payload.Status == "" {
			continue
		}
		if payload.Status == "error" {
			summary := strings.TrimSpace(payload.Summary)
			if summary == "" {
				summary = "unknown openclaw agent error"
			}
			return "", payload.RunID, errors.New(summary)
		}
		return collectPayloadText(payload), payload.RunID, nil
	}
}

func (c *Client) sessionKeyFor(sessionID string) string {
	base := strings.TrimSpace(c.defaultSessionKey)
	id := strings.TrimSpace(sessionID)
	mainKey := ""
	switch {
	case base == "" && id == "":
		mainKey = "alfredo"
	case base == "":
		mainKey = id
	case id == "":
		mainKey = base
	default:
		mainKey = base + ":" + id
	}

	agentID := normalizeAgentID(c.defaultAgentID)
	if agentID == "" {
		return mainKey
	}

	normalizedMainKey := strings.ToLower(strings.TrimSpace(mainKey))
	if normalizedMainKey == "" || normalizedMainKey == "main" {
		return "agent:" + agentID + ":main"
	}
	if strings.HasPrefix(normalizedMainKey, "agent:") {
		return normalizedMainKey
	}
	return "agent:" + agentID + ":" + normalizedMainKey
}

func isGatewayRestartError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "close 1012") || strings.Contains(msg, "service restart")
}

func collectPayloadText(payload openClawAgentPayload) string {
	if len(payload.Result.Payloads) == 0 {
		summary := strings.TrimSpace(payload.Summary)
		if isPlaceholderAssistantText(summary) {
			return ""
		}
		return summary
	}

	lines := make([]string, 0, len(payload.Result.Payloads)*2)
	seen := make(map[string]struct{}, len(payload.Result.Payloads)*2)
	for _, raw := range payload.Result.Payloads {
		for _, candidate := range extractTextFromPayload(raw) {
			cleaned := strings.TrimSpace(strings.ReplaceAll(candidate, "[[reply_to_current]]", ""))
			if cleaned == "" || isPlaceholderAssistantText(cleaned) {
				continue
			}
			if _, ok := seen[cleaned]; ok {
				continue
			}
			seen[cleaned] = struct{}{}
			lines = append(lines, cleaned)
		}
	}
	if len(lines) == 0 {
		summary := strings.TrimSpace(payload.Summary)
		if isPlaceholderAssistantText(summary) {
			return ""
		}
		return summary
	}
	return strings.Join(lines, "\n")
}

func extractTextFromPayload(raw json.RawMessage) []string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	return extractTextValuesRecursive(payload, 0)
}

func extractTextValuesRecursive(node any, depth int) []string {
	if depth > 8 {
		return nil
	}

	switch value := node.(type) {
	case map[string]any:
		out := make([]string, 0, 4)
		for key, child := range value {
			switch typed := child.(type) {
			case string:
				switch strings.ToLower(strings.TrimSpace(key)) {
				case "text", "output_text", "value":
					out = append(out, typed)
				}
			default:
				out = append(out, extractTextValuesRecursive(typed, depth+1)...)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			out = append(out, extractTextValuesRecursive(item, depth+1)...)
		}
		return out
	default:
		return nil
	}
}

func isPlaceholderAssistantText(text string) bool {
	value := strings.ToLower(strings.TrimSpace(text))
	if value == "" {
		return true
	}
	switch value {
	case "completed", "accepted", "ok", "done", "no_reply", "no reply", "none", "null":
		return true
	}
	if strings.HasPrefix(value, "[[audio_as_voice]]") {
		return true
	}
	if strings.HasPrefix(value, "media:") {
		return true
	}
	return false
}

func buildVoiceModeMessage(text string) string {
	return voiceReplyPolicyPrompt + "\n\nUser request:\n" + strings.TrimSpace(text)
}

func removeTextOnlyStyleLines(text string) string {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		item := strings.TrimSpace(line)
		if item == "" {
			continue
		}
		if isTextOnlyStyleLine(item) {
			continue
		}
		kept = append(kept, item)
	}
	return strings.Join(kept, "\n")
}

func isTextOnlyStyleLine(line string) bool {
	for _, pattern := range textOnlyStylePatterns {
		if pattern.MatchString(line) {
			return true
		}
	}
	return false
}

func normalizeModelText(text string) string {
	value := strings.TrimSpace(text)
	if value == "" {
		return ""
	}

	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Map(func(r rune) rune {
		switch r {
		case '\u200B', '\u200C', '\u200D', '\u2060', '\uFEFF':
			return -1
		case '\u00A0', '\u202F', '\u2007', '\u205F':
			return ' '
		default:
			return r
		}
	}, value)

	value = strings.ReplaceAll(value, "’", "'")
	value = strings.ReplaceAll(value, "“", "\"")
	value = strings.ReplaceAll(value, "”", "\"")
	value = strings.ReplaceAll(value, "**", "")
	value = strings.ReplaceAll(value, "__", "")
	value = strings.ReplaceAll(value, "`", "")

	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = multiSpaceRE.ReplaceAllString(line, " ")
		line = contractionRE.ReplaceAllString(line, "$1'$2")
		line = spaceBeforePunctRE.ReplaceAllString(line, "$1")
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func readFrame(ctx context.Context, conn *websocket.Conn) (gatewayFrame, error) {
	for {
		if err := ctx.Err(); err != nil {
			return gatewayFrame{}, err
		}
		if deadline, ok := ctx.Deadline(); ok {
			if err := conn.SetReadDeadline(deadline); err != nil {
				return gatewayFrame{}, err
			}
		} else if err := conn.SetReadDeadline(time.Time{}); err != nil {
			return gatewayFrame{}, err
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			return gatewayFrame{}, err
		}

		var frame gatewayFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			continue
		}
		return frame, nil
	}
}

func readConnectChallengeNonce(ctx context.Context, conn *websocket.Conn) (string, error) {
	frame, err := readFrame(ctx, conn)
	if err != nil {
		return "", err
	}
	if frame.Type != "event" || frame.Event != "connect.challenge" {
		return "", nil
	}

	var payload struct {
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		return "", nil
	}
	return strings.TrimSpace(payload.Nonce), nil
}

func randomID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(buf)
}

type gatewayReqFrame struct {
	Type   string      `json:"type"`
	ID     string      `json:"id"`
	Method string      `json:"method"`
	Params interface{} `json:"params,omitempty"`
}

type gatewayFrame struct {
	Type    string            `json:"type"`
	ID      string            `json:"id,omitempty"`
	Event   string            `json:"event,omitempty"`
	OK      bool              `json:"ok,omitempty"`
	Payload json.RawMessage   `json:"payload,omitempty"`
	Error   *gatewayErrorBody `json:"error,omitempty"`
}

func (f gatewayFrame) errorMessage() string {
	if f.Error == nil {
		return "unknown error"
	}
	if message := strings.TrimSpace(f.Error.Message); message != "" {
		return message
	}
	if code := strings.TrimSpace(f.Error.Code); code != "" {
		return code
	}
	return "unknown error"
}

type gatewayErrorBody struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type openClawAgentPayload struct {
	RunID   string `json:"runId"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Result  struct {
		Payloads []json.RawMessage `json:"payloads"`
	} `json:"result"`
}

type deviceIdentity struct {
	DeviceID              string
	PublicKeyRawBase64URL string
	PrivateKey            ed25519.PrivateKey
}

type storedDeviceIdentity struct {
	Version       int    `json:"version"`
	DeviceID      string `json:"deviceId"`
	PublicKey     string `json:"publicKey,omitempty"`
	PrivateKey    string `json:"privateKey,omitempty"`
	PublicKeyPEM  string `json:"publicKeyPem,omitempty"`
	PrivateKeyPEM string `json:"privateKeyPem,omitempty"`
	CreatedAt     int64  `json:"createdAtMs"`
}

func loadOrCreateDeviceIdentity() (deviceIdentity, error) {
	path := resolveDeviceIdentityPath()
	if identity, err := loadDeviceIdentityFromFile(path); err == nil {
		return identity, nil
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return deviceIdentity{}, err
	}
	deviceID := deriveDeviceID(pub)
	record, err := buildStoredDeviceIdentity(path, deviceID, pub, priv)
	if err != nil {
		return deviceIdentity{}, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return deviceIdentity{}, err
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return deviceIdentity{}, err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return deviceIdentity{}, err
	}

	return deviceIdentity{
		DeviceID:              deviceID,
		PublicKeyRawBase64URL: base64.RawURLEncoding.EncodeToString(pub),
		PrivateKey:            priv,
	}, nil
}

func loadDeviceIdentityFromFile(path string) (deviceIdentity, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return deviceIdentity{}, err
	}

	var stored storedDeviceIdentity
	if err := json.Unmarshal(raw, &stored); err != nil {
		return deviceIdentity{}, err
	}
	if stored.Version != 1 {
		return deviceIdentity{}, errors.New("unsupported identity version")
	}
	if strings.TrimSpace(stored.PublicKeyPEM) != "" || strings.TrimSpace(stored.PrivateKeyPEM) != "" {
		return loadPEMDeviceIdentity(stored)
	}

	pubRaw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(stored.PublicKey))
	if err != nil {
		return deviceIdentity{}, err
	}
	privRaw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(stored.PrivateKey))
	if err != nil {
		return deviceIdentity{}, err
	}
	if len(pubRaw) != ed25519.PublicKeySize || len(privRaw) != ed25519.PrivateKeySize {
		return deviceIdentity{}, errors.New("invalid key size in identity file")
	}

	derivedID := deriveDeviceID(ed25519.PublicKey(pubRaw))
	if strings.TrimSpace(stored.DeviceID) != derivedID {
		return deviceIdentity{}, errors.New("device id mismatch")
	}
	return deviceIdentity{
		DeviceID:              derivedID,
		PublicKeyRawBase64URL: strings.TrimSpace(stored.PublicKey),
		PrivateKey:            ed25519.PrivateKey(privRaw),
	}, nil
}

func resolveDeviceIdentityPath() string {
	if custom := strings.TrimSpace(os.Getenv("GATEWAY_OPENCLAW_IDENTITY_PATH")); custom != "" {
		return custom
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ".openclaw/identity/device.json"
	}
	openClawPath := filepath.Join(home, ".openclaw", "identity", "device.json")
	legacyPath := filepath.Join(home, ".alfredo-gateway", "device-identity.json")
	switch {
	case fileExists(openClawPath):
		return openClawPath
	case fileExists(legacyPath):
		return legacyPath
	default:
		return openClawPath
	}
}

func deriveDeviceID(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:])
}

type deviceAuthPayloadParams struct {
	deviceID   string
	clientID   string
	clientMode string
	role       string
	scopes     []string
	signedAt   int64
	token      string
	nonce      string
	platform   string
}

func buildDeviceAuthPayload(params deviceAuthPayloadParams) string {
	version := "v1"
	if strings.TrimSpace(params.nonce) != "" {
		version = "v2"
	}

	base := []string{
		version,
		params.deviceID,
		params.clientID,
		params.clientMode,
		params.role,
		strings.Join(params.scopes, ","),
		fmt.Sprintf("%d", params.signedAt),
		params.token,
	}
	if version == "v2" {
		base = append(base, params.nonce)
	}
	return strings.Join(base, "|")
}

func buildDeviceAuthPayloadV3(params deviceAuthPayloadParams) string {
	base := []string{
		"v3",
		params.deviceID,
		params.clientID,
		params.clientMode,
		params.role,
		strings.Join(params.scopes, ","),
		fmt.Sprintf("%d", params.signedAt),
		params.token,
		params.nonce,
		strings.TrimSpace(params.platform),
		"",
	}
	return strings.Join(base, "|")
}

func resolveClientPlatform() string {
	value := strings.TrimSpace(runtime.GOOS)
	if value == "" {
		return "unknown"
	}
	return value
}

func debugJSON(label string, value any) {
	if strings.TrimSpace(os.Getenv("GATEWAY_OPENCLAW_DEBUG")) == "" {
		return
	}
	raw, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: <marshal error: %v>\n", label, err)
		return
	}
	fmt.Fprintf(os.Stderr, "%s: %s\n", label, raw)
}

func buildStoredDeviceIdentity(path, deviceID string, pub ed25519.PublicKey, priv ed25519.PrivateKey) (storedDeviceIdentity, error) {
	if prefersOpenClawIdentityFormat(path) {
		pubPEM, err := marshalPublicKeyPEM(pub)
		if err != nil {
			return storedDeviceIdentity{}, err
		}
		privPEM, err := marshalPrivateKeyPEM(priv)
		if err != nil {
			return storedDeviceIdentity{}, err
		}
		return storedDeviceIdentity{
			Version:       1,
			DeviceID:      deviceID,
			PublicKeyPEM:  pubPEM,
			PrivateKeyPEM: privPEM,
			CreatedAt:     time.Now().UnixMilli(),
		}, nil
	}

	return storedDeviceIdentity{
		Version:    1,
		DeviceID:   deviceID,
		PublicKey:  base64.RawURLEncoding.EncodeToString(pub),
		PrivateKey: base64.RawURLEncoding.EncodeToString(priv),
		CreatedAt:  time.Now().UnixMilli(),
	}, nil
}

func loadPEMDeviceIdentity(stored storedDeviceIdentity) (deviceIdentity, error) {
	pubBlock, _ := pem.Decode([]byte(strings.TrimSpace(stored.PublicKeyPEM)))
	if pubBlock == nil {
		return deviceIdentity{}, errors.New("invalid public key pem")
	}
	pubAny, err := x509.ParsePKIXPublicKey(pubBlock.Bytes)
	if err != nil {
		return deviceIdentity{}, err
	}
	pubRaw, ok := pubAny.(ed25519.PublicKey)
	if !ok {
		return deviceIdentity{}, errors.New("public key is not ed25519")
	}

	privBlock, _ := pem.Decode([]byte(strings.TrimSpace(stored.PrivateKeyPEM)))
	if privBlock == nil {
		return deviceIdentity{}, errors.New("invalid private key pem")
	}
	privAny, err := x509.ParsePKCS8PrivateKey(privBlock.Bytes)
	if err != nil {
		return deviceIdentity{}, err
	}
	privRaw, ok := privAny.(ed25519.PrivateKey)
	if !ok {
		return deviceIdentity{}, errors.New("private key is not ed25519")
	}

	derivedID := deriveDeviceID(pubRaw)
	if strings.TrimSpace(stored.DeviceID) != derivedID {
		return deviceIdentity{}, errors.New("device id mismatch")
	}
	return deviceIdentity{
		DeviceID:              derivedID,
		PublicKeyRawBase64URL: base64.RawURLEncoding.EncodeToString(pubRaw),
		PrivateKey:            privRaw,
	}, nil
}

func marshalPublicKeyPEM(pub ed25519.PublicKey) (string, error) {
	raw, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw})), nil
}

func marshalPrivateKeyPEM(priv ed25519.PrivateKey) (string, error) {
	raw, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})), nil
}

func prefersOpenClawIdentityFormat(path string) bool {
	value := filepath.ToSlash(strings.TrimSpace(path))
	return strings.Contains(value, "/.openclaw/identity/device.json")
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func normalizeAgentID(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		isAlpha := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if isAlpha || isDigit || r == '_' || r == '-' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if lastDash || b.Len() == 0 {
			continue
		}
		b.WriteByte('-')
		lastDash = true
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "main"
	}
	if len(out) > 64 {
		out = strings.Trim(out[:64], "-")
	}
	if out == "" {
		return "main"
	}
	return out
}
