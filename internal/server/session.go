package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"layeh.com/gopus"

	"gateway/internal/audio"
)

const (
	minSpeechFramesPerTurn    = 2
	noiseWarmupFrameCount     = 6
	minSpeechFrameAvgAbs      = 8
	minSpeechFramePeak        = 45
	maxDynamicNoiseBaseline   = 20
	fallbackTurnAvgAbs        = 22
	fallbackTurnPeak          = 220
	interimMinChunkCount      = 2
	interimMinChunkDelta      = 2
	interimMaxTimeout         = 8 * time.Second
	interimDefaultTimeout     = 6 * time.Second
	interimIdleMinTimeout     = 900 * time.Millisecond
	interimIdleMaxTimeout     = 1200 * time.Millisecond
	initialNoAudioGrace       = 7000 * time.Millisecond
	restartTurnDelayAfterDrop = 700 * time.Millisecond
	strongSpeechFrameAvg      = 180
	strongSpeechFramePeak     = 700
	maxTimeoutSpeechRatio     = 0.66
	minTimeoutInterimUpdate   = 2
	sessionIdleTimeout        = 180 * time.Second
	// Ignore wake-word detect events briefly after a fresh websocket connect.
	// This suppresses false wake-ups commonly seen during reconnect/restart windows.
	initialWakeWordIgnoreWindow = 2500 * time.Millisecond
	// Capture a short wake-word turn window, then reply immediately.
	wakeWordCaptureDuration = 700 * time.Millisecond
)

type envelope struct {
	Type        string `json:"type"`
	State       string `json:"state"`
	Mode        string `json:"mode"`
	Text        string `json:"text"`
	Transport   string `json:"transport"`
	Version     int    `json:"version"`
	SessionID   string `json:"session_id"`
	AudioParams struct {
		Format        string `json:"format"`
		SampleRate    int    `json:"sample_rate"`
		Channels      int    `json:"channels"`
		FrameDuration int    `json:"frame_duration"`
	} `json:"audio_params"`
}

type turnBuffer struct {
	id                          uint64
	mode                        string
	wakeWord                    string
	sampleRate                  int
	frameDurationMS             int
	frames                      [][]byte
	audioBytes                  int
	speechDetected              bool
	speechFrameCount            int
	speechStreak                int
	noiseAvgSum                 int64
	noiseAvgCount               int
	maxFrameAvg                 int
	maxFramePeak                int
	interimInFlight             bool
	interimLastAt               time.Time
	interimLastText             string
	interimLastSpeechFrameCount int
	interimFrameLen             int
	interimUpdates              int
	startedAt                   time.Time
}

type Session struct {
	server   *Server
	conn     *websocket.Conn
	req      *http.Request
	id       string
	deviceID string
	clientID string
	protocol string
	remote   string
	joinedAt time.Time
	pipeline turnPipeline

	writeMu sync.Mutex
	mu      sync.Mutex

	closed             bool
	helloSeen          bool
	pendingWakeWord    string
	currentTurn        *turnBuffer
	nextTurnID         uint64
	silenceTimer       *time.Timer
	maxTurnTimer       *time.Timer
	interimIdleTimer   *time.Timer
	activeCancel       context.CancelFunc
	activeTurnID       uint64
	speaking           bool
	pendingRestartTurn bool
	pendingRestartMode string
	clientSampleRate   int
	clientFrameMS      int
	downlinkSampleRate int
	llmPrimed          bool
	vadDecoder         *gopus.Decoder
	vadSampleRate      int
	vadFrameMS         int
	idleTimer          *time.Timer
}

func newSession(server *Server, conn *websocket.Conn, req *http.Request) *Session {
	return &Session{
		server:             server,
		conn:               conn,
		req:                req,
		id:                 newSessionID(),
		deviceID:           req.Header.Get("Device-Id"),
		clientID:           req.Header.Get("Client-Id"),
		protocol:           req.Header.Get("Protocol-Version"),
		remote:             req.RemoteAddr,
		joinedAt:           time.Now(),
		pipeline:           newDefaultTurnPipeline(server),
		clientSampleRate:   16000,
		clientFrameMS:      server.cfg.OpusFrameDuration,
		downlinkSampleRate: server.cfg.DownlinkSampleRate,
	}
}

func (s *Session) run() {
	defer s.close()

	s.conn.SetReadLimit(4 * 1024 * 1024)
	s.conn.SetCloseHandler(func(code int, text string) error {
		log.Printf("session=%s device_id=%s close code=%d text=%q", s.id, s.req.Header.Get("Device-Id"), code, text)
		return nil
	})

	log.Printf(
		"session=%s connected remote=%s device_id=%s client_id=%s protocol=%s",
		s.id,
		s.remote,
		s.deviceID,
		s.clientID,
		s.protocol,
	)
	s.server.registerSession(s)

	s.idleTimer = time.AfterFunc(sessionIdleTimeout, func() {
		log.Printf("session=%s idle timeout (%v), closing", s.id, sessionIdleTimeout)
		_ = s.conn.Close()
	})

	for {
		messageType, payload, err := s.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return
			}
			log.Printf("session=%s read failed: %v", s.id, err)
			return
		}

		s.idleTimer.Reset(sessionIdleTimeout)

		switch messageType {
		case websocket.TextMessage:
			if err := s.handleText(payload); err != nil {
				log.Printf("session=%s text handler failed: %v", s.id, err)
				_ = s.sendAlert("error", truncateForSpeech(err.Error()), "sad")
			}
		case websocket.BinaryMessage:
			if err := s.handleBinary(payload); err != nil {
				log.Printf("session=%s binary handler failed: %v", s.id, err)
				_ = s.sendAlert("error", truncateForSpeech(err.Error()), "sad")
			}
		default:
			log.Printf("session=%s ignored message type=%d", s.id, messageType)
		}
	}
}

func (s *Session) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.silenceTimer != nil {
		s.silenceTimer.Stop()
	}
	if s.maxTurnTimer != nil {
		s.maxTurnTimer.Stop()
	}
	if s.interimIdleTimer != nil {
		s.interimIdleTimer.Stop()
	}
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	if s.activeCancel != nil {
		s.activeCancel()
		s.activeCancel = nil
	}
	s.mu.Unlock()
	s.server.unregisterSession(s.id)

	_ = s.conn.Close()
}

func (s *Session) snapshotDevice() connectedDevice {
	return connectedDevice{
		SessionID:   s.id,
		DeviceID:    s.deviceID,
		ClientID:    s.clientID,
		Protocol:    s.protocol,
		RemoteAddr:  s.remote,
		ConnectedAt: s.joinedAt.UnixMilli(),
	}
}

func (s *Session) handleText(payload []byte) error {
	var msg envelope
	if err := json.Unmarshal(payload, &msg); err != nil {
		return fmt.Errorf("parse json message failed: %w", err)
	}

	switch msg.Type {
	case "hello":
		return s.handleHello(msg)
	case "listen":
		return s.handleListen(msg)
	case "abort":
		return s.handleAbort()
	case "mcp":
		log.Printf("session=%s ignored mcp payload", s.id)
		return nil
	default:
		log.Printf("session=%s ignored message type=%q", s.id, msg.Type)
		return nil
	}
}

func (s *Session) handleHello(msg envelope) error {
	s.mu.Lock()
	s.helloSeen = true
	if msg.AudioParams.SampleRate > 0 {
		s.clientSampleRate = msg.AudioParams.SampleRate
	}
	if msg.AudioParams.FrameDuration > 0 {
		s.clientFrameMS = msg.AudioParams.FrameDuration
	}
	s.resetVADLocked()
	s.mu.Unlock()

	response := map[string]any{
		"type":       "hello",
		"transport":  "websocket",
		"session_id": s.id,
		"audio_params": map[string]any{
			"format":         "opus",
			"sample_rate":    s.downlinkSampleRate,
			"channels":       1,
			"frame_duration": s.server.cfg.OpusFrameDuration,
		},
	}
	if err := s.sendJSON(response); err != nil {
		return err
	}
	s.primeLLM()
	return nil
}

func (s *Session) primeLLM() {
	type llmPrimer interface {
		Prime(ctx context.Context, sessionID string) error
	}

	primer, ok := s.server.llm.(llmPrimer)
	if !ok {
		return
	}

	s.mu.Lock()
	if s.llmPrimed {
		s.mu.Unlock()
		return
	}
	s.llmPrimed = true
	sessionID := s.id
	s.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()

		if err := primer.Prime(ctx, sessionID); err != nil {
			log.Printf("session=%s llm prime failed: %v", sessionID, err)
			return
		}
		log.Printf("session=%s llm prime ok provider=%s", sessionID, s.server.cfg.LLMProvider)
	}()
}

func (s *Session) handleListen(msg envelope) error {
	switch msg.State {
	case "detect":
		wakeWord := strings.TrimSpace(msg.Text)
		s.mu.Lock()
		s.pendingWakeWord = wakeWord
		s.mu.Unlock()
		log.Printf("session=%s wake_word=%q", s.id, wakeWord)
		return nil
	case "start":
		s.handleListenStart(msg.Mode)
		return nil
	case "stop":
		return s.finalizeTurn("device_stop")
	default:
		return fmt.Errorf("unsupported listen state %q", msg.State)
	}
}

func (s *Session) handleListenStart(mode string) {
	normalizedMode := normalizeMode(mode)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.currentTurn != nil {
		currentTurnID := s.currentTurn.id
		currentMode := s.currentTurn.mode
		s.mu.Unlock()
		log.Printf(
			"session=%s listen.start ignored current_turn=%d current_mode=%s requested_mode=%s",
			s.id,
			currentTurnID,
			currentMode,
			normalizedMode,
		)
		return
	}
	if s.activeTurnID != 0 || s.speaking {
		s.pendingRestartTurn = true
		s.pendingRestartMode = normalizedMode
		activeTurnID := s.activeTurnID
		speaking := s.speaking
		s.mu.Unlock()
		log.Printf(
			"session=%s listen.start deferred active_turn=%d speaking=%t mode=%s",
			s.id,
			activeTurnID,
			speaking,
			normalizedMode,
		)
		return
	}
	s.mu.Unlock()

	s.startTurn(normalizedMode)
}

func (s *Session) handleAbort() error {
	s.mu.Lock()
	s.cancelActiveLocked()
	speaking := s.speaking
	s.speaking = false
	s.mu.Unlock()

	if speaking {
		return s.sendJSON(map[string]any{
			"session_id": s.id,
			"type":       "tts",
			"state":      "stop",
		})
	}
	return nil
}

func (s *Session) handleBinary(payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.helloSeen {
		return errors.New("received audio before hello handshake")
	}
	if s.currentTurn == nil {
		return nil
	}

	frame := append([]byte(nil), payload...)
	s.currentTurn.frames = append(s.currentTurn.frames, frame)
	s.currentTurn.audioBytes += len(frame)
	frameHasSpeech, framePeak, frameAvg := s.frameHasSpeechLocked(
		s.currentTurn,
		frame,
		s.currentTurn.sampleRate,
		s.currentTurn.frameDurationMS,
	)
	if framePeak > s.currentTurn.maxFramePeak {
		s.currentTurn.maxFramePeak = framePeak
	}
	if frameAvg > s.currentTurn.maxFrameAvg {
		s.currentTurn.maxFrameAvg = frameAvg
	}
	if frameHasSpeech {
		s.currentTurn.speechFrameCount++
		s.currentTurn.speechStreak++
		if s.currentTurn.speechStreak >= minSpeechFramesPerTurn {
			s.currentTurn.speechDetected = true
		}
		if (s.currentTurn.mode == "auto" || s.currentTurn.mode == "realtime") &&
			strings.TrimSpace(s.currentTurn.wakeWord) == "" {
			s.armSilenceTimerLocked(s.currentTurn.id)
		}
	} else {
		s.currentTurn.speechStreak = 0
	}
	s.maybeTriggerInterimSTTLocked()
	return nil
}

func (s *Session) startTurn(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cancelActiveLocked()
	s.nextTurnID++
	s.currentTurn = &turnBuffer{
		id:              s.nextTurnID,
		mode:            normalizeMode(mode),
		wakeWord:        s.pendingWakeWord,
		sampleRate:      s.clientSampleRate,
		frameDurationMS: s.clientFrameMS,
		startedAt:       time.Now(),
	}
	s.pendingWakeWord = ""
	if s.silenceTimer != nil {
		s.silenceTimer.Stop()
	}
	if s.maxTurnTimer != nil {
		s.maxTurnTimer.Stop()
	}
	if s.interimIdleTimer != nil {
		s.interimIdleTimer.Stop()
	}
	s.resetVADLocked()
	s.armMaxTurnTimerLocked(s.currentTurn.id)
	if strings.TrimSpace(s.currentTurn.wakeWord) != "" {
		s.armWakeWordFinalizeTimerLocked(s.currentTurn.id)
	} else if s.currentTurn.mode == "auto" || s.currentTurn.mode == "realtime" {
		s.armSilenceTimerLocked(s.currentTurn.id)
	}
	log.Printf("session=%s turn=%d start mode=%s wake_word=%q", s.id, s.currentTurn.id, s.currentTurn.mode, s.currentTurn.wakeWord)
}

func (s *Session) finalizeTurn(reason string) error {
	turn, ctx, err := s.detachTurn(reason)
	if err != nil {
		if errors.Is(err, errNoTurn) {
			return nil
		}
		return err
	}
	go s.processTurn(ctx, turn, reason)
	return nil
}

var errNoTurn = errors.New("no active turn")

func (s *Session) detachTurn(reason string) (*turnBuffer, context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentTurn == nil {
		return nil, nil, errNoTurn
	}
	wakeTurn := strings.TrimSpace(s.currentTurn.wakeWord) != ""
	if len(s.currentTurn.frames) == 0 && !wakeTurn {
		log.Printf(
			"session=%s turn=%d drop reason=%s no_audio frames=0 audio_bytes=%d speech_frames=%d",
			s.id,
			s.currentTurn.id,
			reason,
			s.currentTurn.audioBytes,
			s.currentTurn.speechFrameCount,
		)
		s.currentTurn = nil
		if s.silenceTimer != nil {
			s.silenceTimer.Stop()
		}
		if s.maxTurnTimer != nil {
			s.maxTurnTimer.Stop()
		}
		if s.interimIdleTimer != nil {
			s.interimIdleTimer.Stop()
		}
		return nil, nil, errNoTurn
	}
	peak, avg, sampleCount, hasSpeech := 0, 0, 0, false
	statsErr := error(nil)
	if len(s.currentTurn.frames) > 0 {
		peak, avg, sampleCount, hasSpeech, statsErr = turnPCMStats(s.currentTurn)
	}
	if !wakeTurn {
		if s.currentTurn.speechDetected && statsErr == nil {
			weakFrameEvidence := s.currentTurn.speechFrameCount < minSpeechFramesForStrongEvidence(len(s.currentTurn.frames)) &&
				s.currentTurn.maxFrameAvg < fallbackTurnAvgAbs &&
				s.currentTurn.maxFramePeak < fallbackTurnPeak
			weakSpeechEvidence := s.currentTurn.speechFrameCount < minSpeechFramesForStrongEvidence(len(s.currentTurn.frames)) &&
				s.currentTurn.maxFrameAvg < strongSpeechFrameAvg &&
				s.currentTurn.maxFramePeak < strongSpeechFramePeak
			if weakSpeechEvidence {
				log.Printf(
					"session=%s turn=%d revoke speech by weak evidence frames=%d audio_bytes=%d speech_frames=%d max_frame_peak=%d max_frame_avg=%d pcm_peak=%d pcm_avg=%d pcm_samples=%d pcm_has_speech=%t",
					s.id,
					s.currentTurn.id,
					len(s.currentTurn.frames),
					s.currentTurn.audioBytes,
					s.currentTurn.speechFrameCount,
					s.currentTurn.maxFramePeak,
					s.currentTurn.maxFrameAvg,
					peak,
					avg,
					sampleCount,
					hasSpeech,
				)
				s.currentTurn.speechDetected = false
			}
			if weakFrameEvidence {
				log.Printf(
					"session=%s turn=%d revoke speech by pcm stats frames=%d audio_bytes=%d speech_frames=%d max_frame_peak=%d max_frame_avg=%d pcm_peak=%d pcm_avg=%d pcm_samples=%d",
					s.id,
					s.currentTurn.id,
					len(s.currentTurn.frames),
					s.currentTurn.audioBytes,
					s.currentTurn.speechFrameCount,
					s.currentTurn.maxFramePeak,
					s.currentTurn.maxFrameAvg,
					peak,
					avg,
					sampleCount,
				)
				s.currentTurn.speechDetected = false
			}
		}
		if !s.currentTurn.speechDetected && statsErr == nil {
			if hasSpeech || avg >= fallbackTurnAvgAbs || peak >= fallbackTurnPeak {
				s.currentTurn.speechDetected = true
				log.Printf(
					"session=%s turn=%d speech promoted by turn stats frames=%d audio_bytes=%d speech_frames=%d max_frame_peak=%d max_frame_avg=%d pcm_peak=%d pcm_avg=%d pcm_samples=%d pcm_has_speech=%t",
					s.id,
					s.currentTurn.id,
					len(s.currentTurn.frames),
					s.currentTurn.audioBytes,
					s.currentTurn.speechFrameCount,
					s.currentTurn.maxFramePeak,
					s.currentTurn.maxFrameAvg,
					peak,
					avg,
					sampleCount,
					hasSpeech,
				)
			}
		}
		streamingEnabled := s.runtimeSTTStreamingEnabled()
		if shouldDropMaxTurnTimeoutTurn(
			reason,
			s.currentTurn,
			peak,
			avg,
			hasSpeech,
			streamingEnabled,
		) {
			statsErrText := ""
			if statsErr != nil {
				statsErrText = statsErr.Error()
			}
			log.Printf(
				"session=%s turn=%d drop reason=%s weak_timeout_speech frames=%d audio_bytes=%d speech_frames=%d max_frame_peak=%d max_frame_avg=%d pcm_peak=%d pcm_avg=%d pcm_samples=%d pcm_has_speech=%t interim_updates=%d pcm_err=%q",
				s.id,
				s.currentTurn.id,
				reason,
				len(s.currentTurn.frames),
				s.currentTurn.audioBytes,
				s.currentTurn.speechFrameCount,
				s.currentTurn.maxFramePeak,
				s.currentTurn.maxFrameAvg,
				peak,
				avg,
				sampleCount,
				hasSpeech,
				s.currentTurn.interimUpdates,
				statsErrText,
			)
			s.currentTurn = nil
			if s.silenceTimer != nil {
				s.silenceTimer.Stop()
			}
			if s.maxTurnTimer != nil {
				s.maxTurnTimer.Stop()
			}
			if s.interimIdleTimer != nil {
				s.interimIdleTimer.Stop()
			}
			return nil, nil, errNoTurn
		}
		if !s.currentTurn.speechDetected {
			statsErrText := ""
			if statsErr != nil {
				statsErrText = statsErr.Error()
			}
			log.Printf(
				"session=%s turn=%d drop reason=%s no_speech frames=%d audio_bytes=%d speech_frames=%d max_frame_peak=%d max_frame_avg=%d pcm_peak=%d pcm_avg=%d pcm_samples=%d pcm_has_speech=%t pcm_err=%q",
				s.id,
				s.currentTurn.id,
				reason,
				len(s.currentTurn.frames),
				s.currentTurn.audioBytes,
				s.currentTurn.speechFrameCount,
				s.currentTurn.maxFramePeak,
				s.currentTurn.maxFrameAvg,
				peak,
				avg,
				sampleCount,
				hasSpeech,
				statsErrText,
			)
			s.currentTurn = nil
			if s.silenceTimer != nil {
				s.silenceTimer.Stop()
			}
			if s.maxTurnTimer != nil {
				s.maxTurnTimer.Stop()
			}
			if s.interimIdleTimer != nil {
				s.interimIdleTimer.Stop()
			}
			return nil, nil, errNoTurn
		}
	}
	turn := s.currentTurn
	s.currentTurn = nil
	if s.silenceTimer != nil {
		s.silenceTimer.Stop()
	}
	if s.maxTurnTimer != nil {
		s.maxTurnTimer.Stop()
	}
	if s.interimIdleTimer != nil {
		s.interimIdleTimer.Stop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.activeCancel = cancel
	s.activeTurnID = turn.id
	log.Printf(
		"session=%s turn=%d finalize reason=%s frames=%d audio_bytes=%d speech_frames=%d max_frame_peak=%d max_frame_avg=%d interim_updates=%d",
		s.id,
		turn.id,
		reason,
		len(turn.frames),
		turn.audioBytes,
		turn.speechFrameCount,
		turn.maxFramePeak,
		turn.maxFrameAvg,
		turn.interimUpdates,
	)
	return turn, ctx, nil
}

func (s *Session) processTurn(ctx context.Context, turn *turnBuffer, reason string) {
	defer s.clearActive(turn.id)
	if s.pipeline == nil {
		s.pipeline = newDefaultTurnPipeline(s.server)
	}
	s.pipeline.Run(ctx, s, turn, reason)
}

func (s *Session) maybeTriggerInterimSTTLocked() {
	if s.server == nil {
		return
	}
	if !s.runtimeSTTStreamingEnabled() {
		return
	}
	turn := s.currentTurn
	now := time.Now()
	if !shouldTriggerInterimSTT(now, turn, s.runtimeSTTInterimInterval(), s.runtimeSTTInterimMinAudio()) {
		return
	}

	turnID := turn.id
	sampleRate := turn.sampleRate
	frameDurationMS := turn.frameDurationMS
	speechFrameCount := turn.speechFrameCount
	frames := cloneOpusFrames(turn.frames)
	turn.interimInFlight = true
	turn.interimLastAt = now

	go s.processInterimSTT(turnID, sampleRate, frameDurationMS, speechFrameCount, frames)
}

func shouldTriggerInterimSTT(
	now time.Time,
	turn *turnBuffer,
	interval time.Duration,
	minAudio time.Duration,
) bool {
	if turn == nil {
		return false
	}
	if turn.interimInFlight {
		return false
	}
	if turn.frameDurationMS <= 0 {
		return false
	}

	minFrames := minimumInterimFrames(minAudio, turn.frameDurationMS)
	if minFrames < interimMinChunkCount {
		minFrames = interimMinChunkCount
	}
	if len(turn.frames) < minFrames {
		return false
	}
	if interval > 0 && !turn.interimLastAt.IsZero() && now.Sub(turn.interimLastAt) < interval {
		return false
	}
	if len(turn.frames)-turn.interimFrameLen < interimMinChunkDelta {
		return false
	}
	return true
}

func minimumInterimFrames(minAudio time.Duration, frameDurationMS int) int {
	if frameDurationMS <= 0 {
		return 1
	}
	if minAudio <= 0 {
		return 1
	}
	frameDuration := time.Duration(frameDurationMS) * time.Millisecond
	frames := int((minAudio + frameDuration - 1) / frameDuration)
	if frames < 1 {
		return 1
	}
	return frames
}

func cloneOpusFrames(frames [][]byte) [][]byte {
	if len(frames) == 0 {
		return nil
	}
	cloned := make([][]byte, len(frames))
	for i := range frames {
		cloned[i] = append([]byte(nil), frames[i]...)
	}
	return cloned
}

func (s *Session) processInterimSTT(
	turnID uint64,
	sampleRate, frameDurationMS, speechFrameCount int,
	frames [][]byte,
) {
	transcript, err := s.transcribeFrames(sampleRate, frameDurationMS, frames)
	transcript = strings.TrimSpace(transcript)

	var shouldSend bool
	s.mu.Lock()
	currentTurn := s.currentTurn
	if currentTurn != nil && currentTurn.id == turnID {
		currentTurn.interimInFlight = false
		currentTurn.interimFrameLen = len(frames)
		if err == nil && transcript != "" {
			currentTurn.interimLastSpeechFrameCount = speechFrameCount
			s.armInterimIdleTimerLocked(currentTurn.id)
		}
		if err == nil && transcript != "" && transcript != currentTurn.interimLastText {
			currentTurn.interimLastText = transcript
			currentTurn.interimUpdates++
			canPromoteByEnergy := likelySpeechFromFrameStats(currentTurn)
			if !currentTurn.speechDetected && canPromoteByEnergy {
				currentTurn.speechDetected = true
				if currentTurn.mode == "auto" || currentTurn.mode == "realtime" {
					s.armSilenceTimerLocked(currentTurn.id)
				}
				log.Printf(
					"session=%s turn=%d speech promoted by interim stats max_frame_peak=%d max_frame_avg=%d",
					s.id,
					currentTurn.id,
					currentTurn.maxFramePeak,
					currentTurn.maxFrameAvg,
				)
			}
			if currentTurn.speechDetected || canPromoteByEnergy {
				shouldSend = true
			}
		}
	}
	s.mu.Unlock()

	if err != nil {
		log.Printf("session=%s turn=%d stt interim failed: %v", s.id, turnID, err)
		return
	}
	if !shouldSend {
		return
	}
	s.mu.Lock()
	active := s.currentTurn != nil && s.currentTurn.id == turnID
	s.mu.Unlock()
	if !active {
		return
	}

	if err := s.sendJSON(map[string]any{
		"session_id": s.id,
		"type":       "stt",
		"state":      "interim",
		"text":       transcript,
	}); err != nil {
		log.Printf("session=%s turn=%d send interim stt failed: %v", s.id, turnID, err)
	}
}

func (s *Session) transcribeFrames(sampleRate, frameDurationMS int, frames [][]byte) (string, error) {
	ingress, err := defaultIngressStage{}.Process(&turnBuffer{
		sampleRate:      sampleRate,
		frameDurationMS: frameDurationMS,
		frames:          frames,
	})
	if err != nil {
		return "", err
	}
	if len(ingress.wav) == 0 {
		return "", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.interimSTTTimeout())
	defer cancel()
	return s.server.transcriber.Transcribe(ctx, ingress.wav)
}

func (s *Session) interimSTTTimeout() time.Duration {
	timeout := s.server.cfg.STTTimeout / 2
	if timeout <= 0 {
		timeout = interimDefaultTimeout
	}
	if timeout > interimMaxTimeout {
		timeout = interimMaxTimeout
	}
	return timeout
}

func ellipsizeLogText(text string, limit int) string {
	s := strings.TrimSpace(text)
	if limit <= 0 || len(s) <= limit {
		return s
	}
	if limit <= 3 {
		return s[:limit]
	}
	return s[:limit-3] + "..."
}

func (s *Session) sendTTSStart(ctx context.Context, turnID uint64) error {
	s.mu.Lock()
	s.speaking = true
	s.mu.Unlock()

	return s.sendPipelineJSON(ctx, turnID, map[string]any{
		"session_id": s.id,
		"type":       "tts",
		"state":      "start",
	})
}

func (s *Session) sendTTSStop(turnID uint64) {
	s.mu.Lock()
	if !s.speaking {
		s.mu.Unlock()
		return
	}
	active := s.activeTurnID == turnID
	s.speaking = false
	s.mu.Unlock()
	if !active {
		log.Printf("session=%s turn=%d tts stop sent after turn switched", s.id, turnID)
	}
	_ = s.sendJSON(map[string]any{
		"session_id": s.id,
		"type":       "tts",
		"state":      "stop",
	})
}

func (s *Session) clearActive(turnID uint64) {
	s.mu.Lock()
	if s.activeTurnID != turnID {
		s.mu.Unlock()
		return
	}
	if s.activeCancel != nil {
		s.activeCancel()
		s.activeCancel = nil
	}
	s.activeTurnID = 0
	s.speaking = false
	if s.interimIdleTimer != nil {
		s.interimIdleTimer.Stop()
	}
	restartTurn := s.pendingRestartTurn
	restartMode := s.pendingRestartMode
	s.pendingRestartTurn = false
	s.pendingRestartMode = ""
	s.mu.Unlock()

	if restartTurn {
		go s.restartTurnIfIdleAfterDelay(restartMode, restartTurnDelayAfterDrop)
	}
}

func (s *Session) requestTurnRestart(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.pendingRestartTurn = true
	s.pendingRestartMode = normalizeMode(mode)
}

func (s *Session) restartTurnIfIdleAfterDelay(mode string, delay time.Duration) {
	if delay > 0 {
		time.Sleep(delay)
	}
	s.mu.Lock()
	if s.closed || s.activeTurnID != 0 || s.currentTurn != nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.startTurn(mode)
}

func (s *Session) cancelActiveLocked() {
	if s.silenceTimer != nil {
		s.silenceTimer.Stop()
	}
	if s.maxTurnTimer != nil {
		s.maxTurnTimer.Stop()
	}
	if s.interimIdleTimer != nil {
		s.interimIdleTimer.Stop()
	}
	if s.activeCancel != nil {
		s.activeCancel()
		s.activeCancel = nil
	}
	s.activeTurnID = 0
	s.currentTurn = nil
	s.resetVADLocked()
}

func (s *Session) resetVADLocked() {
	s.vadDecoder = nil
	s.vadSampleRate = 0
	s.vadFrameMS = 0
}

func (s *Session) ensureVADLocked(sampleRate, frameDurationMS int) bool {
	if sampleRate <= 0 || frameDurationMS <= 0 {
		return false
	}
	if s.vadDecoder != nil && s.vadSampleRate == sampleRate && s.vadFrameMS == frameDurationMS {
		return true
	}

	decoder, err := gopus.NewDecoder(sampleRate, 1)
	if err != nil {
		log.Printf("session=%s create vad decoder failed: %v", s.id, err)
		s.resetVADLocked()
		return false
	}

	s.vadDecoder = decoder
	s.vadSampleRate = sampleRate
	s.vadFrameMS = frameDurationMS
	return true
}

func (s *Session) frameHasSpeechLocked(
	turn *turnBuffer,
	frame []byte,
	sampleRate,
	frameDurationMS int,
) (bool, int, int) {
	if len(frame) == 0 {
		return false, 0, 0
	}
	if !s.ensureVADLocked(sampleRate, frameDurationMS) {
		return true, 0, 0
	}

	frameSize := sampleRate * frameDurationMS / 1000
	if frameSize <= 0 {
		return true, 0, 0
	}
	decoded, err := s.vadDecoder.Decode(frame, frameSize, false)
	if err != nil {
		log.Printf("session=%s vad decode failed: %v", s.id, err)
		return true, 0, 0
	}

	peak, avg := pcmFrameStats(decoded)
	if turn == nil {
		return avg >= minSpeechFrameAvgAbs || peak >= minSpeechFramePeak, peak, avg
	}

	if turn.noiseAvgCount < noiseWarmupFrameCount {
		turn.noiseAvgSum += int64(avg)
		turn.noiseAvgCount++
	}
	noiseBaseline := 0
	if turn.noiseAvgCount > 0 {
		noiseBaseline = int(turn.noiseAvgSum / int64(turn.noiseAvgCount))
	}
	if noiseBaseline > maxDynamicNoiseBaseline {
		noiseBaseline = maxDynamicNoiseBaseline
	}

	avgThreshold := noiseBaseline + 4
	if avgThreshold < minSpeechFrameAvgAbs {
		avgThreshold = minSpeechFrameAvgAbs
	}
	peakThreshold := noiseBaseline*3 + 35
	if peakThreshold < minSpeechFramePeak {
		peakThreshold = minSpeechFramePeak
	}

	isSpeech := avg >= avgThreshold || peak >= peakThreshold
	return isSpeech, peak, avg
}

func (s *Session) armSilenceTimerLocked(turnID uint64) {
	if s.silenceTimer != nil {
		s.silenceTimer.Stop()
	}
	timeout := s.runtimeSessionSilence()
	if turn := s.currentTurn; turn != nil && turn.id == turnID && len(turn.frames) == 0 {
		if timeout < initialNoAudioGrace {
			timeout = initialNoAudioGrace
		}
	}
	s.silenceTimer = time.AfterFunc(timeout, func() {
		s.mu.Lock()
		current := s.currentTurn
		s.mu.Unlock()
		if current == nil || current.id != turnID {
			return
		}
		if err := s.finalizeTurn("silence_timeout"); err != nil && !errors.Is(err, errNoTurn) {
			log.Printf("session=%s turn=%d auto finalize failed: %v", s.id, turnID, err)
		}
	})
}

func (s *Session) armWakeWordFinalizeTimerLocked(turnID uint64) {
	if s.silenceTimer != nil {
		s.silenceTimer.Stop()
	}
	s.silenceTimer = time.AfterFunc(wakeWordCaptureDuration, func() {
		s.mu.Lock()
		current := s.currentTurn
		s.mu.Unlock()
		if current == nil || current.id != turnID {
			return
		}
		if err := s.finalizeTurn("wake_word_capture_timeout"); err != nil && !errors.Is(err, errNoTurn) {
			log.Printf("session=%s turn=%d wake-word finalize failed: %v", s.id, turnID, err)
		}
	})
}

func (s *Session) armMaxTurnTimerLocked(turnID uint64) {
	if s.maxTurnTimer != nil {
		s.maxTurnTimer.Stop()
	}
	maxTurn := s.runtimeSessionMaxTurn()
	if maxTurn <= 0 {
		return
	}
	s.maxTurnTimer = time.AfterFunc(maxTurn, func() {
		s.mu.Lock()
		current := s.currentTurn
		s.mu.Unlock()
		if current == nil || current.id != turnID {
			return
		}
		if err := s.finalizeTurn("max_turn_timeout"); err != nil && !errors.Is(err, errNoTurn) {
			log.Printf("session=%s turn=%d max-turn finalize failed: %v", s.id, turnID, err)
		}
	})
}

func (s *Session) armInterimIdleTimerLocked(turnID uint64) {
	if s.interimIdleTimer != nil {
		s.interimIdleTimer.Stop()
	}
	idle := s.runtimeSTTInterimInterval()
	if idle <= 0 {
		idle = interimIdleMinTimeout
	}
	if idle < interimIdleMinTimeout {
		idle = interimIdleMinTimeout
	}
	if idle > interimIdleMaxTimeout {
		idle = interimIdleMaxTimeout
	}
	if silence := s.runtimeSessionSilence(); silence > 0 && idle > silence {
		idle = silence
	}
	s.interimIdleTimer = time.AfterFunc(idle, func() {
		s.mu.Lock()
		current := s.currentTurn
		interimUpdates := 0
		interimInFlight := false
		if current != nil && current.id == turnID {
			interimUpdates = current.interimUpdates
			interimInFlight = current.interimInFlight
		}
		s.mu.Unlock()
		if current == nil || current.id != turnID {
			return
		}
		// Only apply after there was interim speech evidence.
		if interimUpdates <= 0 || interimInFlight {
			return
		}
		if err := s.finalizeTurn("interim_idle_timeout"); err != nil && !errors.Is(err, errNoTurn) {
			log.Printf("session=%s turn=%d interim-idle finalize failed: %v", s.id, turnID, err)
		}
	})
}

func (s *Session) sendPipelineJSON(ctx context.Context, turnID uint64, payload map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	active := s.activeTurnID == turnID
	s.mu.Unlock()
	if !active {
		return context.Canceled
	}
	return s.sendJSON(payload)
}

func (s *Session) sendBinary(ctx context.Context, turnID uint64, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	active := s.activeTurnID == turnID
	s.mu.Unlock()
	if !active {
		return context.Canceled
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, payload)
}

func (s *Session) sendJSON(payload map[string]any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.WriteJSON(payload)
}

func (s *Session) sendAlert(status, message, emotion string) error {
	return s.sendJSON(map[string]any{
		"session_id": s.id,
		"type":       "alert",
		"status":     status,
		"message":    truncateForSpeech(message),
		"emotion":    emotion,
	})
}

func (s *Session) sendSystemCommand(command string, config map[string]any, reboot bool) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return errors.New("system command is empty")
	}
	payload := map[string]any{
		"session_id": s.id,
		"type":       "system",
		"command":    command,
	}
	if config != nil {
		payload["config"] = config
	}
	if reboot {
		payload["reboot"] = true
	}
	return s.sendJSON(payload)
}

func normalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "realtime":
		return "realtime"
	case "manual":
		return "manual"
	default:
		return "auto"
	}
}

func newSessionID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func guessEmotion(text string) string {
	value := strings.ToLower(text)
	switch {
	case strings.Contains(value, "sorry"), strings.Contains(value, "regret"):
		return "sad"
	case strings.Contains(value, "!"), strings.Contains(value, "great"), strings.Contains(value, "awesome"):
		return "happy"
	default:
		return "neutral"
	}
}

func (s *Session) runtimeSessionSilence() time.Duration {
	fallback := 0 * time.Millisecond
	if s.server != nil {
		fallback = s.server.cfg.SessionSilence
	}
	ms := s.runtimeConfigSnapshot().SessionSilenceMS
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func (s *Session) runtimeSessionMaxTurn() time.Duration {
	fallback := 0 * time.Millisecond
	if s.server != nil {
		fallback = s.server.cfg.SessionMaxTurn
	}
	ms := s.runtimeConfigSnapshot().SessionMaxTurnMS
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func (s *Session) runtimeSTTStreamingEnabled() bool {
	fallback := false
	if s.server != nil {
		fallback = s.server.cfg.STTStreamingEnabled
	}
	return runtimeBoolWithFallback(s.runtimeConfigSnapshot(), fallback)
}

func (s *Session) runtimeSTTInterimInterval() time.Duration {
	fallback := 0 * time.Millisecond
	if s.server != nil {
		fallback = s.server.cfg.STTInterimInterval
	}
	ms := s.runtimeConfigSnapshot().STTInterimIntervalMS
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func (s *Session) runtimeSTTInterimMinAudio() time.Duration {
	fallback := 0 * time.Millisecond
	if s.server != nil {
		fallback = s.server.cfg.STTInterimMinAudio
	}
	ms := s.runtimeConfigSnapshot().STTInterimMinAudioMS
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func (s *Session) runtimeConfigSnapshot() runtimeConfig {
	if s.server == nil {
		return runtimeConfig{}
	}
	runtime := s.server.getRuntimeConfig()
	if runtimeLooksUninitialized(runtime) {
		return defaultRuntimeConfig(s.server.cfg)
	}
	return runtime
}

func runtimeBoolWithFallback(runtime runtimeConfig, fallback bool) bool {
	if runtimeLooksUninitialized(runtime) {
		return fallback
	}
	return runtime.STTStreamingEnabled
}

func runtimeLooksUninitialized(runtime runtimeConfig) bool {
	return runtime.Model == "" &&
		runtime.Effort == "" &&
		runtime.Verbosity == "" &&
		runtime.ContextMessages == 0 &&
		runtime.MemoryRecallDays == 0 &&
		runtime.TTSVoice == "" &&
		runtime.TTSRate == 0 &&
		runtime.SessionSilenceMS == 0 &&
		runtime.SessionMaxTurnMS == 0 &&
		runtime.STTInterimIntervalMS == 0 &&
		runtime.STTInterimMinAudioMS == 0
}

func truncateForSpeech(text string) string {
	value := strings.TrimSpace(text)
	runes := []rune(value)
	if len(runes) <= 160 {
		return value
	}
	return string(runes[:160])
}

func pcmFrameStats(samples []int16) (peak int, avg int) {
	if len(samples) == 0 {
		return 0, 0
	}
	var sumAbs int64
	for _, sample := range samples {
		value := int(sample)
		if value < 0 {
			value = -value
		}
		sumAbs += int64(value)
		if value > peak {
			peak = value
		}
	}
	avg = int(sumAbs / int64(len(samples)))
	return peak, avg
}

func likelySpeechFromFrameStats(turn *turnBuffer) bool {
	if turn == nil {
		return false
	}
	if turn.maxFrameAvg >= fallbackTurnAvgAbs {
		return true
	}
	return turn.maxFramePeak >= fallbackTurnPeak && turn.maxFrameAvg >= minSpeechFrameAvgAbs/2
}

func shouldDropMaxTurnTimeoutTurn(
	reason string,
	turn *turnBuffer,
	pcmPeak int,
	pcmAvg int,
	pcmHasSpeech bool,
	sttStreamingEnabled bool,
) bool {
	if reason != "max_turn_timeout" || turn == nil || len(turn.frames) == 0 {
		return false
	}
	if turnSpeechFrameRatio(turn) >= maxTimeoutSpeechRatio {
		return false
	}
	if sttStreamingEnabled && turn.interimUpdates >= minTimeoutInterimUpdate {
		return false
	}

	strongStats := turn.maxFrameAvg >= strongSpeechFrameAvg ||
		turn.maxFramePeak >= strongSpeechFramePeak ||
		pcmAvg >= strongSpeechFrameAvg ||
		pcmPeak >= strongSpeechFramePeak ||
		pcmHasSpeech
	return strongStats
}

func minSpeechFramesForStrongEvidence(totalFrames int) int {
	minFrames := totalFrames / 6
	if minFrames < 4 {
		return 4
	}
	return minFrames
}

func turnPCMStats(turn *turnBuffer) (peak int, avg int, sampleCount int, hasSpeech bool, err error) {
	if turn == nil {
		return 0, 0, 0, false, nil
	}
	decoded, err := audio.DecodeOpusFrames(turn.frames, turn.sampleRate, turn.frameDurationMS)
	if err != nil {
		return 0, 0, 0, false, err
	}
	if len(decoded) == 0 {
		return 0, 0, 0, false, nil
	}
	hasSpeech = audio.PCMHasSpeech(decoded)
	var sumAbs int64
	for _, sample := range decoded {
		value := int(sample)
		if value < 0 {
			value = -value
		}
		sumAbs += int64(value)
		if value > peak {
			peak = value
		}
	}
	avg = int(sumAbs / int64(len(decoded)))
	return peak, avg, len(decoded), hasSpeech, nil
}
