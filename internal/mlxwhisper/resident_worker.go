package mlxwhisper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	workerResponseLineMax = 2 << 20 // 2MB
	workerStderrTailBytes = 8 << 10 // 8KB
	workerStopTimeout     = 800 * time.Millisecond
)

type residentWorkerOptions struct {
	MLXWhisperBin string
	Model         string
	ReadyTimeout  time.Duration
}

type residentWorker struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	reqMu     sync.Mutex
	closeOnce sync.Once
	nextID    uint64

	responses chan workerResponse
	stopped   chan struct{}
	exitMu    sync.RWMutex
	exitErr   error

	stderrTail *tailBuffer
}

type workerRequest struct {
	Op       string `json:"op"`
	ID       string `json:"id,omitempty"`
	AudioB64 string `json:"audio_b64,omitempty"`
	Language string `json:"language,omitempty"`
}

type workerResponse struct {
	Event string `json:"event,omitempty"`
	ID    string `json:"id,omitempty"`
	OK    bool   `json:"ok"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

func newResidentWorker(options residentWorkerOptions) (*residentWorker, error) {
	bin := strings.TrimSpace(options.MLXWhisperBin)
	if bin == "" {
		bin = "mlx_whisper"
	}
	if options.ReadyTimeout <= 0 {
		options.ReadyTimeout = 8 * time.Second
	}
	model := strings.TrimSpace(options.Model)
	if model == "" {
		model = "mlx-community/whisper-large-v3-turbo"
	}

	pythonBin, err := resolvePythonInterpreter(bin)
	if err != nil {
		return nil, fmt.Errorf("resolve mlx-whisper python runtime failed: %w", err)
	}

	cmd := exec.Command(pythonBin, "-u", "-c", residentWorkerPython)
	cmd.Env = append(
		os.Environ(),
		"PYTORCH_ENABLE_MPS_FALLBACK=1",
		"MLX_WHISPER_MODEL="+model,
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open resident worker stdin failed: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open resident worker stdout failed: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open resident worker stderr failed: %w", err)
	}

	worker := &residentWorker{
		cmd:        cmd,
		stdin:      stdin,
		responses:  make(chan workerResponse, 16),
		stopped:    make(chan struct{}),
		stderrTail: &tailBuffer{maxBytes: workerStderrTailBytes},
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start resident worker failed: %w", err)
	}
	go worker.readStdout(stdout)
	go worker.readStderr(stderr)
	go worker.waitForExit()

	if err := worker.waitReady(options.ReadyTimeout); err != nil {
		_ = worker.Close()
		return nil, err
	}
	return worker, nil
}

func (w *residentWorker) Transcribe(ctx context.Context, wav []byte, language string) (string, error) {
	if len(wav) == 0 {
		return "", errors.New("stt audio payload is empty")
	}

	w.reqMu.Lock()
	defer w.reqMu.Unlock()

	if err := w.processErr(); err != nil {
		return "", err
	}

	reqID := fmt.Sprintf("%d", atomic.AddUint64(&w.nextID, 1))
	req := workerRequest{
		Op:       "transcribe",
		ID:       reqID,
		AudioB64: base64.StdEncoding.EncodeToString(wav),
	}
	lang := strings.TrimSpace(language)
	if lang != "" {
		req.Language = lang
	}
	line, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal resident stt request failed: %w", err)
	}
	line = append(line, '\n')
	if _, err := w.stdin.Write(line); err != nil {
		return "", fmt.Errorf("write resident stt request failed: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("resident stt request canceled: %w", ctx.Err())
		case <-w.stopped:
			return "", w.processErr()
		case resp, ok := <-w.responses:
			if !ok {
				return "", w.processErr()
			}
			if resp.Event == "ready" {
				continue
			}
			if resp.ID != reqID {
				continue
			}
			if !resp.OK {
				errText := strings.TrimSpace(resp.Error)
				if errText == "" {
					errText = "unknown resident stt error"
				}
				return "", errors.New(errText)
			}
			text := strings.TrimSpace(resp.Text)
			if text == "" {
				return "", errors.New("mlx-whisper returned empty text")
			}
			return text, nil
		}
	}
}

func (w *residentWorker) Close() error {
	var closeErr error
	w.closeOnce.Do(func() {
		w.reqMu.Lock()
		defer w.reqMu.Unlock()

		shutdownLine := []byte(`{"op":"shutdown"}` + "\n")
		_, _ = w.stdin.Write(shutdownLine)
		_ = w.stdin.Close()

		select {
		case <-w.stopped:
		case <-time.After(workerStopTimeout):
			if w.cmd.Process != nil {
				_ = w.cmd.Process.Kill()
			}
			<-w.stopped
		}
		w.exitMu.RLock()
		exitErr := w.exitErr
		w.exitMu.RUnlock()
		closeErr = normalizeProcessCloseErr(exitErr)
	})
	return closeErr
}

func (w *residentWorker) waitReady(timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		select {
		case <-deadline.C:
			return fmt.Errorf(
				"resident stt worker startup timeout after %s (stderr: %s)",
				timeout.Round(time.Millisecond),
				w.stderrTail.String(),
			)
		case <-w.stopped:
			return w.processErr()
		case resp, ok := <-w.responses:
			if !ok {
				return w.processErr()
			}
			if resp.Event != "ready" {
				continue
			}
			if !resp.OK {
				errText := strings.TrimSpace(resp.Error)
				if errText == "" {
					errText = "resident stt worker failed during startup"
				}
				return errors.New(errText)
			}
			return nil
		}
	}
}

func (w *residentWorker) readStdout(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), workerResponseLineMax)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var resp workerResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue
		}
		w.responses <- resp
	}
	close(w.responses)
}

func (w *residentWorker) readStderr(stderr io.Reader) {
	_, _ = io.Copy(w.stderrTail, stderr)
}

func (w *residentWorker) waitForExit() {
	err := w.cmd.Wait()
	w.exitMu.Lock()
	w.exitErr = err
	w.exitMu.Unlock()
	close(w.stopped)
}

func (w *residentWorker) processErr() error {
	w.exitMu.RLock()
	err := w.exitErr
	w.exitMu.RUnlock()
	if err == nil {
		select {
		case <-w.stopped:
			err = errors.New("resident stt worker exited")
		default:
			return nil
		}
	}
	stderr := strings.TrimSpace(w.stderrTail.String())
	if stderr != "" {
		return fmt.Errorf("resident stt worker stopped: %v (stderr: %s)", err, stderr)
	}
	return fmt.Errorf("resident stt worker stopped: %v", err)
}

func resolvePythonInterpreter(bin string) (string, error) {
	binPath, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("mlx-whisper binary not found: %w", err)
	}

	content, err := os.ReadFile(binPath)
	if err != nil {
		return "", fmt.Errorf("read mlx-whisper wrapper failed: %w", err)
	}
	if len(content) == 0 {
		return "", errors.New("mlx-whisper wrapper is empty")
	}

	firstLine := content
	if idx := bytes.IndexByte(content, '\n'); idx >= 0 {
		firstLine = content[:idx]
	}
	line := strings.TrimSpace(string(firstLine))
	if !strings.HasPrefix(line, "#!") {
		return "", errors.New("mlx-whisper wrapper has no shebang")
	}
	spec := strings.TrimSpace(strings.TrimPrefix(line, "#!"))
	if spec == "" {
		return "", errors.New("mlx-whisper wrapper has empty shebang interpreter")
	}

	parts := strings.Fields(spec)
	if len(parts) == 0 {
		return "", errors.New("mlx-whisper wrapper has invalid shebang interpreter")
	}
	if parts[0] == "/usr/bin/env" {
		if len(parts) < 2 {
			return "", errors.New("mlx-whisper wrapper shebang env is missing command")
		}
		pythonBin, err := exec.LookPath(parts[1])
		if err != nil {
			return "", fmt.Errorf("resolve shebang env interpreter failed: %w", err)
		}
		return pythonBin, nil
	}
	return parts[0], nil
}

func normalizeProcessCloseErr(err error) error {
	if err == nil {
		return nil
	}
	errText := strings.ToLower(err.Error())
	if strings.Contains(errText, "signal: killed") {
		return nil
	}
	if strings.Contains(errText, "exit status 0") {
		return nil
	}
	return err
}

type tailBuffer struct {
	mu       sync.Mutex
	maxBytes int
	buf      []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.maxBytes <= 0 {
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.maxBytes {
		b.buf = b.buf[len(b.buf)-b.maxBytes:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

const residentWorkerPython = `
import base64
import json
import os
import sys
import tempfile

import mlx_whisper

MODEL = os.environ.get("MLX_WHISPER_MODEL", "mlx-community/whisper-large-v3-turbo")

def emit(payload):
    sys.stdout.write(json.dumps(payload, ensure_ascii=False) + "\n")
    sys.stdout.flush()

emit({"event": "ready", "ok": True})

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    req_id = ""
    try:
        req = json.loads(line)
        op = req.get("op", "")
        req_id = str(req.get("id", ""))
        if op == "shutdown":
            emit({"ok": True, "event": "shutdown"})
            break
        if op != "transcribe":
            emit({"id": req_id, "ok": False, "error": "unsupported op"})
            continue

        audio_b64 = req.get("audio_b64", "")
        language = str(req.get("language", "") or "").strip() or None
        audio = base64.b64decode(audio_b64.encode("utf-8"), validate=False)
        if not audio:
            emit({"id": req_id, "ok": False, "error": "empty audio"})
            continue

        with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as tmp:
            tmp.write(audio)
            tmp_path = tmp.name

        try:
            result = mlx_whisper.transcribe(
                tmp_path,
                path_or_hf_repo=MODEL,
                task="transcribe",
                language=language,
                verbose=False,
            )
            text = str(result.get("text", "")).strip()
            if not text:
                emit({"id": req_id, "ok": False, "error": "mlx-whisper returned empty text"})
            else:
                emit({"id": req_id, "ok": True, "text": text})
        finally:
            try:
                os.remove(tmp_path)
            except OSError:
                pass
    except Exception as err:
        emit({"id": req_id, "ok": False, "error": str(err)})
`
