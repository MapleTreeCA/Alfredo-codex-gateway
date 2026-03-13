package server

import (
	"context"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gateway/internal/audio"
)

type turnPipeline interface {
	Run(ctx context.Context, session *Session, turn *turnBuffer, reason string)
}

type ingressStage interface {
	Process(turn *turnBuffer) (pipelineIngressResult, error)
}

type sttStage interface {
	Process(ctx context.Context, wav []byte) (string, error)
}

type llmStage interface {
	Process(ctx context.Context, sessionID, transcript string, options llmGenerationOptions) (string, error)
}

type ttsStage interface {
	Process(ctx context.Context, text string, options ttsSynthesisOptions) ([]byte, error)
}

type downlinkStage interface {
	Process(wav []byte, sampleRate, frameDurationMS, bitrate int) (pipelineDownlinkResult, error)
}

type pipelineIngressResult struct {
	wav []byte
}

type pipelineDownlinkResult struct {
	frames     [][]byte
	sampleRate int
}

type llmGenerationOptions struct {
	Model            string
	Effort           string
	Verbosity        string
	Online           bool
	Concise          bool
	ContextMessages  int
	MemoryRecallDays int
}

type ttsSynthesisOptions struct {
	Voice string
	Rate  int
}

type defaultTurnPipeline struct {
	ingress  ingressStage
	stt      sttStage
	llm      llmStage
	tts      ttsStage
	downlink downlinkStage
}

var sentenceChunkPattern = regexp.MustCompile(`[^.!?。！？]+[.!?。！？]*`)

const ttsStreamingChunkMaxRunes = 120

var wakeWordAutoReplyChunks = []string{
	"Hi there.",
	"I'm listening.",
}

const systemCommandStandby = "standby"
const llmFailureFallbackText = "I got stuck for a moment. Please try again."

var standbyVoiceCommandSet = map[string]struct{}{
	"go sleep":    {},
	"go to sleep": {},
	"sleep":       {},
	"stop":        {},
	"shutup":      {},
	"shut up":     {},
}

type ttsChunkResult struct {
	chunk    string
	wav      []byte
	duration time.Duration
	err      error
}

func newDefaultTurnPipeline(server *Server) turnPipeline {
	return &defaultTurnPipeline{
		ingress:  defaultIngressStage{},
		stt:      defaultSTTStage{transcriber: server.transcriber},
		llm:      defaultLLMStage{client: server.llm},
		tts:      defaultTTSStage{synthesizer: server.synthesizer},
		downlink: defaultDownlinkStage{},
	}
}

func (p *defaultTurnPipeline) Run(ctx context.Context, session *Session, turn *turnBuffer, reason string) {
	if shouldUseWakeWordAutoReply(turn) {
		p.runWakeWordAutoReply(ctx, session, turn, reason)
		return
	}

	stageStartedAt := time.Now()

	ingress, err := p.ingress.Process(turn)
	if err != nil {
		_ = session.sendAlert("error", "Failed to decode microphone audio", "sad")
		log.Printf("session=%s turn=%d decode failed: %v", session.id, turn.id, err)
		return
	}
	if len(ingress.wav) == 0 {
		return
	}

	stageStartedAt = time.Now()
	transcript := ""
	reusedInterim := false
	if candidate, ok := shouldReuseInterimTranscriptForFinal(turn, reason); ok {
		transcript = candidate
		reusedInterim = true
	} else {
		var err error
		transcript, err = p.stt.Process(ctx, ingress.wav)
		if err != nil {
			_ = session.sendAlert("error", truncateForSpeech(err.Error()), "sad")
			log.Printf("session=%s turn=%d stt failed: %v", session.id, turn.id, err)
			return
		}
	}
	rawTranscript := transcript
	if !reusedInterim && shouldRetryWeakTranscript(turn, transcript) {
		retryText, retryErr := retryTranscriptWithStrongGain(ctx, p.stt, turn)
		if retryErr != nil {
			log.Printf("session=%s turn=%d stt retry failed: %v", session.id, turn.id, retryErr)
		} else if strings.TrimSpace(retryText) != "" {
			log.Printf(
				"session=%s turn=%d stt retry replace old=%q new=%q",
				session.id,
				turn.id,
				ellipsizeLogText(transcript, 80),
				ellipsizeLogText(retryText, 80),
			)
			transcript = retryText
		}
	}
	collapsedTranscript := collapseRepeatedSentences(transcript)
	if collapsedTranscript != transcript {
		log.Printf(
			"session=%s turn=%d stt transcript collapsed old=%q new=%q",
			session.id,
			turn.id,
			ellipsizeLogText(transcript, 80),
			ellipsizeLogText(collapsedTranscript, 80),
		)
		transcript = collapsedTranscript
	}
	if shouldDropClippedWakeTurn(turn) {
		log.Printf(
			"session=%s turn=%d drop reason=clipped_wake_turn transcript=%q speech_frames=%d max_frame_peak=%d max_frame_avg=%d ratio=%.2f interim_updates=%d wake_word=%q",
			session.id,
			turn.id,
			ellipsizeLogText(transcript, 80),
			turn.speechFrameCount,
			turn.maxFramePeak,
			turn.maxFrameAvg,
			turnSpeechFrameRatio(turn),
			turn.interimUpdates,
			turn.wakeWord,
		)
		session.requestTurnRestart(turn.mode)
		return
	}
	if shouldDropLanguageMismatchTranscript(session.server.cfg.STTLanguage, turn, transcript) {
		log.Printf(
			"session=%s turn=%d drop reason=language_mismatch expected_lang=%q transcript=%q speech_frames=%d max_frame_peak=%d max_frame_avg=%d ratio=%.2f",
			session.id,
			turn.id,
			session.server.cfg.STTLanguage,
			ellipsizeLogText(transcript, 80),
			turn.speechFrameCount,
			turn.maxFramePeak,
			turn.maxFrameAvg,
			turnSpeechFrameRatio(turn),
		)
		session.requestTurnRestart(turn.mode)
		return
	}
	if shouldDropLikelyHallucinatedTranscript(turn, transcript) {
		log.Printf(
			"session=%s turn=%d drop reason=likely_hallucination transcript=%q speech_frames=%d max_frame_peak=%d max_frame_avg=%d ratio=%.2f",
			session.id,
			turn.id,
			ellipsizeLogText(transcript, 80),
			turn.speechFrameCount,
			turn.maxFramePeak,
			turn.maxFrameAvg,
			turnSpeechFrameRatio(turn),
		)
		session.requestTurnRestart(turn.mode)
		return
	}
	if shouldDropRepeatedLoopTranscript(turn, rawTranscript, transcript) {
		run, total := repeatedSentenceRunStats(rawTranscript)
		log.Printf(
			"session=%s turn=%d drop reason=repeated_loop_hallucination transcript=%q raw=%q repeat_run=%d repeat_total=%d speech_frames=%d max_frame_peak=%d max_frame_avg=%d ratio=%.2f interim_updates=%d",
			session.id,
			turn.id,
			ellipsizeLogText(transcript, 80),
			ellipsizeLogText(rawTranscript, 80),
			run,
			total,
			turn.speechFrameCount,
			turn.maxFramePeak,
			turn.maxFrameAvg,
			turnSpeechFrameRatio(turn),
			turn.interimUpdates,
		)
		session.requestTurnRestart(turn.mode)
		return
	}
	if shouldDropSevereClippedNoiseTurn(turn) {
		log.Printf(
			"session=%s turn=%d drop reason=severe_clipped_noise transcript=%q speech_frames=%d max_frame_peak=%d max_frame_avg=%d ratio=%.2f interim_updates=%d wake_word=%q",
			session.id,
			turn.id,
			ellipsizeLogText(transcript, 80),
			turn.speechFrameCount,
			turn.maxFramePeak,
			turn.maxFrameAvg,
			turnSpeechFrameRatio(turn),
			turn.interimUpdates,
			turn.wakeWord,
		)
		session.requestTurnRestart(turn.mode)
		return
	}
	if shouldDropSilenceTimeoutTranscript(reason, turn, transcript) {
		log.Printf(
			"session=%s turn=%d drop reason=silence_timeout_low_conf transcript=%q speech_frames=%d max_frame_peak=%d max_frame_avg=%d ratio=%.2f interim_updates=%d wake_word=%q",
			session.id,
			turn.id,
			ellipsizeLogText(transcript, 80),
			turn.speechFrameCount,
			turn.maxFramePeak,
			turn.maxFrameAvg,
			turnSpeechFrameRatio(turn),
			turn.interimUpdates,
			turn.wakeWord,
		)
		session.requestTurnRestart(turn.mode)
		return
	}
	if shouldDropMaxTurnTimeoutTranscript(reason, turn, transcript) {
		log.Printf(
			"session=%s turn=%d drop reason=max_turn_timeout_low_conf transcript=%q speech_frames=%d max_frame_peak=%d max_frame_avg=%d ratio=%.2f interim_updates=%d",
			session.id,
			turn.id,
			ellipsizeLogText(transcript, 80),
			turn.speechFrameCount,
			turn.maxFramePeak,
			turn.maxFrameAvg,
			turnSpeechFrameRatio(turn),
			turn.interimUpdates,
		)
		session.requestTurnRestart(turn.mode)
		return
	}
	if reusedInterim {
		log.Printf(
			"session=%s turn=%d stt reused_interim duration=%s text=%q interim_updates=%d interim_frames=%d total_frames=%d",
			session.id,
			turn.id,
			time.Since(stageStartedAt).Round(time.Millisecond),
			ellipsizeLogText(transcript, 120),
			turn.interimUpdates,
			turn.interimFrameLen,
			len(turn.frames),
		)
	} else {
		log.Printf(
			"session=%s turn=%d stt ok duration=%s text=%q",
			session.id,
			turn.id,
			time.Since(stageStartedAt).Round(time.Millisecond),
			ellipsizeLogText(transcript, 120),
		)
	}
	if err := session.sendPipelineJSON(ctx, turn.id, map[string]any{
		"session_id": session.id,
		"type":       "stt",
		"state":      "final",
		"text":       transcript,
	}); err != nil {
		return
	}
	if matched, command := shouldShortcutToStandby(transcript); matched {
		if err := session.sendSystemCommand(systemCommandStandby, nil, false); err != nil {
			_ = session.sendAlert("error", "Failed to enter standby", "sad")
			log.Printf(
				"session=%s turn=%d standby shortcut failed command=%q transcript=%q: %v",
				session.id,
				turn.id,
				command,
				ellipsizeLogText(transcript, 120),
				err,
			)
			return
		}
		log.Printf(
			"session=%s turn=%d standby shortcut hit command=%q transcript=%q",
			session.id,
			turn.id,
			command,
			ellipsizeLogText(transcript, 120),
		)
		return
	}

	stageStartedAt = time.Now()
	runtime := session.server.getRuntimeConfig()
	llmOptions := llmGenerationOptions{
		Model:            runtime.Model,
		Effort:           runtime.Effort,
		Verbosity:        runtime.Verbosity,
		Online:           runtime.Online,
		Concise:          runtime.Concise,
		ContextMessages:  runtime.ContextMessages,
		MemoryRecallDays: runtime.MemoryRecallDays,
	}
	reply, err := p.llm.Process(ctx, session.id, transcript, llmOptions)
	if err != nil {
		_ = session.sendAlert("error", truncateForSpeech(err.Error()), "sad")
		log.Printf(
			"session=%s turn=%d llm provider=%s model=%s context_messages=%d memory_recall_days=%d online=%t failed: %v",
			session.id,
			turn.id,
			session.server.cfg.LLMProvider,
			llmOptions.Model,
			llmOptions.ContextMessages,
			llmOptions.MemoryRecallDays,
			llmOptions.Online,
			err,
		)
		if fallbackErr := p.runLLMFailureFallback(ctx, session, turn, runtime, llmFailureFallbackText); fallbackErr != nil {
			log.Printf("session=%s turn=%d llm fallback failed: %v", session.id, turn.id, fallbackErr)
		}
		return
	}
	log.Printf(
		"session=%s turn=%d llm ok provider=%s model=%s context_messages=%d memory_recall_days=%d online=%t duration=%s text=%q",
		session.id,
		turn.id,
		session.server.cfg.LLMProvider,
		llmOptions.Model,
		llmOptions.ContextMessages,
		llmOptions.MemoryRecallDays,
		llmOptions.Online,
		time.Since(stageStartedAt).Round(time.Millisecond),
		ellipsizeLogText(reply, 160),
	)
	if err := session.sendPipelineJSON(ctx, turn.id, map[string]any{
		"session_id": session.id,
		"type":       "llm",
		"emotion":    guessEmotion(reply),
		"text":       reply,
	}); err != nil {
		return
	}
	spokenText := reply
	if truncated, ok := truncateTTSTextByMaxOutputTokens(reply, runtime.MaxOutputTokens); ok {
		log.Printf(
			"session=%s turn=%d tts text truncated max_output_tokens=%d original_chars=%d spoken_chars=%d",
			session.id,
			turn.id,
			runtime.MaxOutputTokens,
			utf8.RuneCountInString(reply),
			utf8.RuneCountInString(truncated),
		)
		spokenText = truncated
	}

	if err := session.sendTTSStart(ctx, turn.id); err != nil {
		return
	}
	defer session.sendTTSStop(turn.id)

	if err := session.sendPipelineJSON(ctx, turn.id, map[string]any{
		"session_id": session.id,
		"type":       "tts",
		"state":      "sentence_start",
		"text":       spokenText,
	}); err != nil {
		return
	}

	ttsOptions := ttsSynthesisOptions{
		Voice: runtime.TTSVoice,
		Rate:  runtime.TTSRate,
	}
	ttsChunks := splitTTSTextForStreaming(spokenText, ttsStreamingChunkMaxRunes)
	if len(ttsChunks) == 0 {
		ttsChunks = []string{spokenText}
	}

	remainingFrameBudget := -1
	if maxDuration := session.server.cfg.TTSMaxDuration; maxDuration > 0 && session.server.cfg.OpusFrameDuration > 0 {
		frameStep := time.Duration(session.server.cfg.OpusFrameDuration) * time.Millisecond
		maxFrames := int(maxDuration / frameStep)
		if maxDuration%frameStep != 0 {
			maxFrames++
		}
		if maxFrames < 1 {
			maxFrames = 1
		}
		remainingFrameBudget = maxFrames
	}

	ttsStreamStartedAt := time.Now()
	totalWAVBytes := 0
	totalOpusFrames := 0
	totalSentFrames := 0
	firstAudioDelay := time.Duration(0)
	firstFrameSent := false
	truncatedByDuration := false
	truncatedChunkIndex := 0
	truncatedOriginalFrames := 0

	ttsCtx, cancelTTS := context.WithCancel(ctx)
	defer cancelTTS()

	currentChunk := startTTSChunkSynthesis(ttsCtx, p.tts, ttsChunks[0], ttsOptions)
	for chunkIndex := 0; chunkIndex < len(ttsChunks); chunkIndex++ {
		chunkResult, waitErr := awaitTTSChunkSynthesis(ctx, currentChunk)
		if waitErr != nil {
			_ = session.sendAlert("error", truncateForSpeech(waitErr.Error()), "sad")
			log.Printf("session=%s turn=%d tts failed: %v", session.id, turn.id, waitErr)
			return
		}
		var nextChunk <-chan ttsChunkResult
		if chunkIndex+1 < len(ttsChunks) {
			nextChunk = startTTSChunkSynthesis(ttsCtx, p.tts, ttsChunks[chunkIndex+1], ttsOptions)
		}
		if chunkResult.err != nil {
			_ = session.sendAlert("error", truncateForSpeech(chunkResult.err.Error()), "sad")
			log.Printf(
				"session=%s turn=%d tts chunk=%d/%d failed: %v",
				session.id,
				turn.id,
				chunkIndex+1,
				len(ttsChunks),
				chunkResult.err,
			)
			return
		}
		totalWAVBytes += len(chunkResult.wav)
		log.Printf(
			"session=%s turn=%d tts chunk=%d/%d ok duration=%s wav_bytes=%d chunk_chars=%d voice=%q rate=%d",
			session.id,
			turn.id,
			chunkIndex+1,
			len(ttsChunks),
			chunkResult.duration.Round(time.Millisecond),
			len(chunkResult.wav),
			utf8.RuneCountInString(chunkResult.chunk),
			ttsOptions.Voice,
			ttsOptions.Rate,
		)

		downlink, err := p.downlink.Process(
			chunkResult.wav,
			session.downlinkSampleRate,
			session.server.cfg.OpusFrameDuration,
			session.server.cfg.DownlinkOpusBitrate,
		)
		if err != nil {
			_ = session.sendAlert("error", truncateForSpeech(err.Error()), "sad")
			log.Printf("session=%s turn=%d downlink failed: %v", session.id, turn.id, err)
			return
		}
		totalOpusFrames += len(downlink.frames)
		log.Printf(
			"session=%s turn=%d downlink chunk=%d/%d opus_frames=%d sample_rate=%d bitrate=%d",
			session.id,
			turn.id,
			chunkIndex+1,
			len(ttsChunks),
			len(downlink.frames),
			downlink.sampleRate,
			session.server.cfg.DownlinkOpusBitrate,
		)

		framesToSend := downlink.frames
		if remainingFrameBudget >= 0 {
			if remainingFrameBudget <= 0 {
				truncatedByDuration = true
				truncatedChunkIndex = chunkIndex + 1
				truncatedOriginalFrames = len(downlink.frames)
				break
			}
			if len(framesToSend) > remainingFrameBudget {
				truncatedByDuration = true
				truncatedChunkIndex = chunkIndex + 1
				truncatedOriginalFrames = len(downlink.frames)
				framesToSend = framesToSend[:remainingFrameBudget]
			}
			remainingFrameBudget -= len(framesToSend)
		}

		for i, frame := range framesToSend {
			if err := session.sendBinary(ctx, turn.id, frame); err != nil {
				return
			}
			if !firstFrameSent {
				firstFrameSent = true
				firstAudioDelay = time.Since(ttsStreamStartedAt)
			}
			// Stream frames at real-time pace to avoid overflowing device playback
			// queues when replies are long (for example, slower TTS rates).
			if i < len(framesToSend)-1 && session.server.cfg.OpusFrameDuration > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(session.server.cfg.OpusFrameDuration) * time.Millisecond):
				}
			}
		}
		totalSentFrames += len(framesToSend)
		if truncatedByDuration {
			break
		}
		currentChunk = nextChunk
	}

	if truncatedByDuration {
		log.Printf(
			"session=%s turn=%d tts downlink truncated max_duration=%s frame_ms=%d chunk=%d original_frames=%d sent_frames=%d",
			session.id,
			turn.id,
			session.server.cfg.TTSMaxDuration.Round(time.Millisecond),
			session.server.cfg.OpusFrameDuration,
			truncatedChunkIndex,
			truncatedOriginalFrames,
			totalSentFrames,
		)
	}
	if !firstFrameSent {
		firstAudioDelay = time.Since(ttsStreamStartedAt)
	}
	log.Printf(
		"session=%s turn=%d tts stream ok chunks=%d first_audio_delay=%s wav_bytes=%d opus_frames=%d sent_frames=%d duration=%s voice=%q rate=%d truncated=%t",
		session.id,
		turn.id,
		len(ttsChunks),
		firstAudioDelay.Round(time.Millisecond),
		totalWAVBytes,
		totalOpusFrames,
		totalSentFrames,
		time.Since(ttsStreamStartedAt).Round(time.Millisecond),
		ttsOptions.Voice,
		ttsOptions.Rate,
		truncatedByDuration,
	)

	log.Printf(
		"session=%s turn=%d done reason=%s transcript_bytes=%d reply_chars=%d duration=%s",
		session.id,
		turn.id,
		reason,
		len(transcript),
		len(reply),
		time.Since(turn.startedAt).Round(time.Millisecond),
	)
}

func shouldUseWakeWordAutoReply(turn *turnBuffer) bool {
	if turn == nil {
		return false
	}
	return strings.TrimSpace(turn.wakeWord) != ""
}

func (p *defaultTurnPipeline) runWakeWordAutoReply(
	ctx context.Context,
	session *Session,
	turn *turnBuffer,
	reason string,
) {
	runtime := session.server.getRuntimeConfig()
	transcript := strings.TrimSpace(turn.wakeWord)
	reply := strings.Join(wakeWordAutoReplyChunks, " ")
	// Wake-word gating/noise filtering should happen on device side.
	// Backend must always honor an explicit wake-word turn and play local TTS ack.

	log.Printf(
		"session=%s turn=%d wake_word auto reply enabled wake_word=%q reason=%s",
		session.id,
		turn.id,
		transcript,
		reason,
	)

	if err := session.sendPipelineJSON(ctx, turn.id, map[string]any{
		"session_id": session.id,
		"type":       "stt",
		"state":      "final",
		"text":       transcript,
	}); err != nil {
		return
	}

	if err := session.sendPipelineJSON(ctx, turn.id, map[string]any{
		"session_id": session.id,
		"type":       "llm",
		"emotion":    "neutral",
		"text":       reply,
	}); err != nil {
		return
	}

	if err := session.sendTTSStart(ctx, turn.id); err != nil {
		return
	}
	defer session.sendTTSStop(turn.id)

	if err := session.sendPipelineJSON(ctx, turn.id, map[string]any{
		"session_id": session.id,
		"type":       "tts",
		"state":      "sentence_start",
		"text":       reply,
	}); err != nil {
		return
	}

	ttsOptions := ttsSynthesisOptions{
		Voice: runtime.TTSVoice,
		Rate:  runtime.TTSRate,
	}

	totalFrames := 0
	totalWavBytes := 0
	for i, chunk := range wakeWordAutoReplyChunks {
		stageStartedAt := time.Now()
		wav, err := p.tts.Process(ctx, chunk, ttsOptions)
		if err != nil {
			_ = session.sendAlert("error", truncateForSpeech(err.Error()), "sad")
			log.Printf("session=%s turn=%d wake_word tts chunk=%d/%d failed: %v", session.id, turn.id, i+1, len(wakeWordAutoReplyChunks), err)
			return
		}
		totalWavBytes += len(wav)
		log.Printf(
			"session=%s turn=%d wake_word tts chunk=%d/%d ok duration=%s wav_bytes=%d text=%q voice=%q rate=%d",
			session.id,
			turn.id,
			i+1,
			len(wakeWordAutoReplyChunks),
			time.Since(stageStartedAt).Round(time.Millisecond),
			len(wav),
			chunk,
			ttsOptions.Voice,
			ttsOptions.Rate,
		)

		downlink, err := p.downlink.Process(
			wav,
			session.downlinkSampleRate,
			session.server.cfg.OpusFrameDuration,
			session.server.cfg.DownlinkOpusBitrate,
		)
		if err != nil {
			_ = session.sendAlert("error", truncateForSpeech(err.Error()), "sad")
			log.Printf("session=%s turn=%d wake_word downlink chunk=%d/%d failed: %v", session.id, turn.id, i+1, len(wakeWordAutoReplyChunks), err)
			return
		}
		totalFrames += len(downlink.frames)
		for frameIndex, frame := range downlink.frames {
			if err := session.sendBinary(ctx, turn.id, frame); err != nil {
				return
			}
			if frameIndex < len(downlink.frames)-1 && session.server.cfg.OpusFrameDuration > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(session.server.cfg.OpusFrameDuration) * time.Millisecond):
				}
			}
		}
	}

	log.Printf(
		"session=%s turn=%d wake_word done reason=%s wake_word=%q reply=%q tts_chunks=%d wav_bytes=%d opus_frames=%d duration=%s",
		session.id,
		turn.id,
		reason,
		transcript,
		reply,
		len(wakeWordAutoReplyChunks),
		totalWavBytes,
		totalFrames,
		time.Since(turn.startedAt).Round(time.Millisecond),
	)
}

func (p *defaultTurnPipeline) runLLMFailureFallback(
	ctx context.Context,
	session *Session,
	turn *turnBuffer,
	runtime runtimeConfig,
	fallback string,
) error {
	text := strings.TrimSpace(fallback)
	if text == "" {
		return nil
	}
	if err := session.sendPipelineJSON(ctx, turn.id, map[string]any{
		"session_id": session.id,
		"type":       "llm",
		"emotion":    "sad",
		"text":       text,
	}); err != nil {
		return err
	}
	if err := session.sendTTSStart(ctx, turn.id); err != nil {
		return err
	}
	defer session.sendTTSStop(turn.id)
	if err := session.sendPipelineJSON(ctx, turn.id, map[string]any{
		"session_id": session.id,
		"type":       "tts",
		"state":      "sentence_start",
		"text":       text,
	}); err != nil {
		return err
	}
	ttsOptions := ttsSynthesisOptions{
		Voice: runtime.TTSVoice,
		Rate:  runtime.TTSRate,
	}
	wav, err := p.tts.Process(ctx, text, ttsOptions)
	if err != nil {
		return err
	}
	downlink, err := p.downlink.Process(
		wav,
		session.downlinkSampleRate,
		session.server.cfg.OpusFrameDuration,
		session.server.cfg.DownlinkOpusBitrate,
	)
	if err != nil {
		return err
	}
	for i, frame := range downlink.frames {
		if err := session.sendBinary(ctx, turn.id, frame); err != nil {
			return err
		}
		if i < len(downlink.frames)-1 && session.server.cfg.OpusFrameDuration > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(session.server.cfg.OpusFrameDuration) * time.Millisecond):
			}
		}
	}
	return nil
}

func shouldReuseInterimTranscriptForFinal(turn *turnBuffer, reason string) (string, bool) {
	if turn == nil {
		return "", false
	}
	if reason == "max_turn_timeout" {
		return "", false
	}
	if turn.interimInFlight || turn.interimUpdates <= 0 {
		return "", false
	}
	text := strings.TrimSpace(turn.interimLastText)
	if text == "" {
		return "", false
	}
	if turn.interimFrameLen <= 0 || turn.interimFrameLen > len(turn.frames) {
		return "", false
	}
	// Only reuse when no additional speech was detected after the interim snapshot.
	if turn.speechFrameCount > turn.interimLastSpeechFrameCount {
		return "", false
	}
	tailFrames := len(turn.frames) - turn.interimFrameLen
	if tailFrames < 0 {
		return "", false
	}
	maxTailFrames := minimumInterimFrames(1200*time.Millisecond, turn.frameDurationMS) + 2
	if tailFrames > maxTailFrames {
		return "", false
	}
	return text, true
}

type defaultIngressStage struct{}

func (defaultIngressStage) Process(turn *turnBuffer) (pipelineIngressResult, error) {
	pcm, err := audio.DecodeOpusFrames(turn.frames, turn.sampleRate, turn.frameDurationMS)
	if err != nil {
		return pipelineIngressResult{}, err
	}
	if len(pcm) == 0 {
		return pipelineIngressResult{}, nil
	}

	inputSamples := pcm
	inputRate := turn.sampleRate
	if inputRate != 16000 {
		inputSamples = audio.ResampleLinear(inputSamples, inputRate, 16000)
		inputRate = 16000
	}
	inputSamples = applyAdaptiveInputGain(inputSamples)

	wav, err := audio.EncodeWAV(inputRate, inputSamples)
	if err != nil {
		return pipelineIngressResult{}, err
	}
	return pipelineIngressResult{wav: wav}, nil
}

type defaultSTTStage struct {
	transcriber transcriber
}

func (s defaultSTTStage) Process(ctx context.Context, wav []byte) (string, error) {
	return s.transcriber.Transcribe(ctx, wav)
}

type defaultLLMStage struct {
	client llmClient
}

func (s defaultLLMStage) Process(
	ctx context.Context,
	sessionID, transcript string,
	options llmGenerationOptions,
) (string, error) {
	if llmWithOptions, ok := s.client.(llmOptionClient); ok {
		return llmWithOptions.GenerateWithOptions(ctx, sessionID, transcript, map[string]string{
			"model":              strings.TrimSpace(options.Model),
			"effort":             strings.TrimSpace(options.Effort),
			"verbosity":          strings.TrimSpace(options.Verbosity),
			"context_messages":   strconv.Itoa(options.ContextMessages),
			"memory_recall_days": strconv.Itoa(options.MemoryRecallDays),
			"online":             strconv.FormatBool(options.Online),
			"concise":            strconv.FormatBool(options.Concise),
		})
	}
	return s.client.Generate(ctx, sessionID, transcript)
}

type defaultTTSStage struct {
	synthesizer speechSynthesizer
}

func (s defaultTTSStage) Process(ctx context.Context, text string, options ttsSynthesisOptions) ([]byte, error) {
	if synthWithOptions, ok := s.synthesizer.(speechOptionSynthesizer); ok {
		ttsOptions := map[string]string{}
		if voice := strings.TrimSpace(options.Voice); voice != "" {
			ttsOptions["voice"] = voice
		}
		if options.Rate > 0 {
			ttsOptions["rate"] = strconv.Itoa(options.Rate)
		}
		return synthWithOptions.SynthesizeWithOptions(ctx, text, ttsOptions)
	}
	return s.synthesizer.Synthesize(ctx, text)
}

type defaultDownlinkStage struct{}

func (defaultDownlinkStage) Process(
	wav []byte,
	sampleRate, frameDurationMS, bitrate int,
) (pipelineDownlinkResult, error) {
	parsed, err := audio.ParseWAV(wav)
	if err != nil {
		return pipelineDownlinkResult{}, err
	}
	mono := audio.MixToMono(parsed)
	if mono.SampleRate != sampleRate {
		mono.Samples = audio.ResampleLinear(mono.Samples, mono.SampleRate, sampleRate)
		mono.SampleRate = sampleRate
	}

	frames, err := audio.EncodeOpusFramesWithBitrate(mono.SampleRate, frameDurationMS, bitrate, mono.Samples)
	if err != nil {
		return pipelineDownlinkResult{}, err
	}
	return pipelineDownlinkResult{
		frames:     frames,
		sampleRate: mono.SampleRate,
	}, nil
}

func startTTSChunkSynthesis(
	ctx context.Context,
	stage ttsStage,
	text string,
	options ttsSynthesisOptions,
) <-chan ttsChunkResult {
	resultCh := make(chan ttsChunkResult, 1)
	chunk := strings.TrimSpace(text)
	go func() {
		startedAt := time.Now()
		wav, err := stage.Process(ctx, chunk, options)
		resultCh <- ttsChunkResult{
			chunk:    chunk,
			wav:      wav,
			duration: time.Since(startedAt),
			err:      err,
		}
	}()
	return resultCh
}

func awaitTTSChunkSynthesis(
	ctx context.Context,
	resultCh <-chan ttsChunkResult,
) (ttsChunkResult, error) {
	select {
	case <-ctx.Done():
		return ttsChunkResult{}, ctx.Err()
	case result := <-resultCh:
		return result, nil
	}
}

func splitTTSTextForStreaming(text string, maxRunes int) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	if maxRunes <= 0 {
		maxRunes = ttsStreamingChunkMaxRunes
	}

	parts := sentenceChunkPattern.FindAllString(trimmed, -1)
	if len(parts) == 0 {
		return splitLongTTSText(trimmed, maxRunes)
	}

	chunks := make([]string, 0, len(parts))
	for _, part := range parts {
		candidate := strings.TrimSpace(part)
		if candidate == "" {
			continue
		}
		if utf8.RuneCountInString(candidate) <= maxRunes {
			chunks = append(chunks, candidate)
			continue
		}
		chunks = append(chunks, splitLongTTSText(candidate, maxRunes)...)
	}
	if len(chunks) == 0 {
		return splitLongTTSText(trimmed, maxRunes)
	}
	return chunks
}

func splitLongTTSText(text string, maxRunes int) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	if maxRunes <= 0 || utf8.RuneCountInString(trimmed) <= maxRunes {
		return []string{trimmed}
	}

	words := strings.Fields(trimmed)
	if len(words) <= 1 {
		runes := []rune(trimmed)
		chunks := make([]string, 0, (len(runes)+maxRunes-1)/maxRunes)
		for len(runes) > 0 {
			n := maxRunes
			if n > len(runes) {
				n = len(runes)
			}
			chunk := strings.TrimSpace(string(runes[:n]))
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			runes = runes[n:]
		}
		return chunks
	}

	chunks := make([]string, 0, len(words))
	current := ""
	currentRunes := 0
	for _, word := range words {
		wordRunes := utf8.RuneCountInString(word)
		if current == "" {
			if wordRunes > maxRunes {
				chunks = append(chunks, splitLongTTSText(word, maxRunes)...)
				continue
			}
			current = word
			currentRunes = wordRunes
			continue
		}
		if currentRunes+1+wordRunes <= maxRunes {
			current += " " + word
			currentRunes += 1 + wordRunes
			continue
		}
		chunks = append(chunks, current)
		if wordRunes > maxRunes {
			chunks = append(chunks, splitLongTTSText(word, maxRunes)...)
			current = ""
			currentRunes = 0
			continue
		}
		current = word
		currentRunes = wordRunes
	}
	if current != "" {
		chunks = append(chunks, current)
	}
	return chunks
}

func truncateTTSTextByMaxOutputTokens(text string, maxOutputTokens int) (string, bool) {
	value := strings.TrimSpace(text)
	if value == "" || maxOutputTokens <= 0 {
		return value, false
	}
	// Approximate 1 token ~= 4 chars for speech truncation guard.
	maxRunes := maxOutputTokens * 4
	if maxRunes < 1 {
		return value, false
	}
	if utf8.RuneCountInString(value) <= maxRunes {
		return value, false
	}

	runes := []rune(value)
	clipped := strings.TrimSpace(string(runes[:maxRunes]))
	if clipped == "" {
		return value, false
	}
	if idx := strings.LastIndexAny(clipped, ".!?。！？\n"); idx >= 0 {
		candidate := strings.TrimSpace(clipped[:idx+1])
		// Keep boundary trimming only when we still keep enough content.
		if utf8.RuneCountInString(candidate) >= maxRunes/2 {
			clipped = candidate
		}
	}
	return clipped, true
}

func applyAdaptiveInputGain(samples []int16) []int16 {
	if len(samples) == 0 {
		return samples
	}
	peak, avg := pcmFrameStats(samples)
	if peak < 80 || avg <= 0 {
		return samples
	}

	const (
		targetAvg = 900.0
		maxGain   = 10.0
		minGain   = 1.35
		int16Max  = 32767.0
		int16Min  = -32768.0
	)

	gain := targetAvg / float64(avg)
	if gain < minGain {
		return samples
	}
	if gain > maxGain {
		gain = maxGain
	}

	out := make([]int16, len(samples))
	for i, sample := range samples {
		value := float64(sample) * gain
		value = math.Max(int16Min, math.Min(int16Max, value))
		out[i] = int16(value)
	}
	return out
}

func shouldDropLikelyHallucinatedTranscript(turn *turnBuffer, transcript string) bool {
	if turn == nil {
		return false
	}
	cleaned := normalizeTranscript(transcript)
	if cleaned == "" {
		return true
	}
	if !isCommonHallucinatedPhrase(cleaned) {
		return false
	}
	// Guard: very short one-word acknowledgements without interim updates are
	// usually replay/echo leakage right after long TTS playback.
	if turn.interimUpdates == 0 &&
		len(turn.frames) <= 14 &&
		turn.speechFrameCount <= 14 &&
		(cleaned == "you" || cleaned == "okay" || cleaned == "ok" || cleaned == "thankyou" || cleaned == "thanks") {
		return true
	}
	// Guard: short, saturated bursts at turn boundary often decode into
	// unstable filler words like "you/ok/thanks" and should be ignored.
	if len(turn.frames) <= 12 &&
		turn.speechFrameCount <= 12 &&
		turn.maxFramePeak >= 32000 &&
		turn.maxFrameAvg >= 9000 {
		return true
	}
	ratio := turnSpeechFrameRatio(turn)
	lowEnergy := turn.maxFramePeak < 280 && turn.maxFrameAvg < 60
	weakCoverage := ratio < 0.35 &&
		turn.maxFramePeak < strongSpeechFramePeak &&
		turn.maxFrameAvg < strongSpeechFrameAvg
	weakEvidence := lowEnergy || weakCoverage
	return weakEvidence
}

func shouldDropLanguageMismatchTranscript(expectedLang string, turn *turnBuffer, transcript string) bool {
	if turn == nil {
		return false
	}
	lang := strings.ToLower(strings.TrimSpace(expectedLang))
	if !strings.HasPrefix(lang, "en") {
		return false
	}
	if !containsHangulOrCJK(transcript) {
		return false
	}
	strongEvidence := turnSpeechFrameRatio(turn) >= 0.45 &&
		(turn.maxFramePeak >= strongSpeechFramePeak || turn.maxFrameAvg >= strongSpeechFrameAvg)
	return !strongEvidence
}

func containsHangulOrCJK(text string) bool {
	for _, r := range text {
		if (r >= 0x1100 && r <= 0x11FF) ||
			(r >= 0x3130 && r <= 0x318F) ||
			(r >= 0xAC00 && r <= 0xD7AF) ||
			(r >= 0x3400 && r <= 0x4DBF) ||
			(r >= 0x4E00 && r <= 0x9FFF) ||
			(r >= 0xF900 && r <= 0xFAFF) {
			return true
		}
	}
	return false
}

func turnSpeechFrameRatio(turn *turnBuffer) float64 {
	if turn == nil || len(turn.frames) == 0 {
		return 0
	}
	return float64(turn.speechFrameCount) / float64(len(turn.frames))
}

func normalizeTranscript(text string) string {
	value := strings.ToLower(strings.TrimSpace(text))
	replacer := strings.NewReplacer(
		".", "",
		",", "",
		"!", "",
		"?", "",
		"'", "",
		"\"", "",
		"“", "",
		"”", "",
		"’", "",
		"。", "",
		"，", "",
		"！", "",
		"？", "",
		" ", "",
		"\n", "",
		"\t", "",
	)
	return replacer.Replace(value)
}

func collapseRepeatedSentences(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}

	parts := sentenceChunkPattern.FindAllString(trimmed, -1)
	if len(parts) == 0 {
		return trimmed
	}

	collapsed := make([]string, 0, len(parts))
	prevNorm := ""
	for _, part := range parts {
		candidate := strings.TrimSpace(part)
		if candidate == "" {
			continue
		}
		norm := normalizeTranscript(candidate)
		if norm == "" {
			continue
		}
		if norm == prevNorm {
			continue
		}
		prevNorm = norm
		collapsed = append(collapsed, candidate)
	}
	if len(collapsed) == 0 {
		return trimmed
	}
	return strings.TrimSpace(strings.Join(collapsed, " "))
}

func shouldShortcutToStandby(transcript string) (bool, string) {
	normalized := normalizeVoiceCommandForIntent(transcript)
	if normalized == "" {
		return false, ""
	}
	parts := strings.Fields(normalized)
	if len(parts) < 2 || parts[0] != "alfredo" {
		return false, ""
	}
	command := strings.Join(parts[1:], " ")
	if _, ok := standbyVoiceCommandSet[command]; ok {
		return true, command
	}
	return false, ""
}

func normalizeVoiceCommandForIntent(text string) string {
	value := strings.ToLower(strings.TrimSpace(text))
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		",", " ",
		".", " ",
		"!", " ",
		"?", " ",
		";", " ",
		":", " ",
		"，", " ",
		"。", " ",
		"！", " ",
		"？", " ",
		"、", " ",
	)
	value = replacer.Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func isCommonHallucinatedPhrase(text string) bool {
	switch text {
	case "thankyou",
		"thanks",
		"thanksforyourwatching",
		"yourewelcome",
		"you",
		"okay",
		"ok",
		"alright",
		"hello",
		"hi",
		"huh",
		"yeah",
		"yep",
		"imsorry",
		"sorry",
		"imgoingtogo",
		"imgoingtogoaheadandgetit",
		"imgoingtogoaheadandgetsomewater":
		return true
	default:
		return false
	}
}

func shouldRetryWeakTranscript(turn *turnBuffer, transcript string) bool {
	if turn == nil {
		return false
	}
	cleaned := normalizeTranscript(transcript)
	if cleaned == "" {
		return false
	}
	if !isCommonHallucinatedPhrase(cleaned) {
		return false
	}
	ratio := turnSpeechFrameRatio(turn)
	return ratio < 0.45 && turn.maxFramePeak < 420 && turn.maxFrameAvg < 100
}

func repeatedSentenceRunStats(text string) (maxRun int, total int) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, 0
	}
	parts := sentenceChunkPattern.FindAllString(trimmed, -1)
	if len(parts) == 0 {
		return 0, 0
	}
	total = 0
	last := ""
	current := 0
	for _, part := range parts {
		norm := normalizeTranscript(part)
		if norm == "" {
			continue
		}
		total++
		if norm == last {
			current++
		} else {
			last = norm
			current = 1
		}
		if current > maxRun {
			maxRun = current
		}
	}
	return maxRun, total
}

func shouldDropRepeatedLoopTranscript(turn *turnBuffer, rawTranscript, collapsedTranscript string) bool {
	if turn == nil {
		return false
	}
	maxRun, total := repeatedSentenceRunStats(rawTranscript)
	if total < 4 || maxRun < 4 {
		return false
	}
	if turn.interimUpdates >= minTimeoutInterimUpdate && turnSpeechFrameRatio(turn) >= maxTimeoutSpeechRatio {
		return false
	}
	cleaned := normalizeTranscript(collapsedTranscript)
	if cleaned == "" {
		return true
	}
	if utf8.RuneCountInString(cleaned) > 32 {
		return false
	}
	if isCommonHallucinatedPhrase(cleaned) {
		return true
	}
	return maxRun >= 6 || turnSpeechFrameRatio(turn) < maxTimeoutSpeechRatio
}

func shouldDropMaxTurnTimeoutTranscript(reason string, turn *turnBuffer, transcript string) bool {
	if reason != "max_turn_timeout" || turn == nil {
		return false
	}
	cleaned := normalizeTranscript(transcript)
	if cleaned == "" {
		return true
	}
	if turn.interimUpdates >= minTimeoutInterimUpdate {
		return false
	}
	if isCommonHallucinatedPhrase(cleaned) {
		return true
	}
	shortTranscript := utf8.RuneCountInString(cleaned) <= 24
	if shortTranscript && turnSpeechFrameRatio(turn) < maxTimeoutSpeechRatio {
		return true
	}
	return false
}

func shouldDropSilenceTimeoutTranscript(reason string, turn *turnBuffer, transcript string) bool {
	if reason != "silence_timeout" || turn == nil {
		return false
	}
	cleaned := normalizeTranscript(transcript)
	if cleaned == "" {
		return true
	}
	if turn.interimUpdates > 0 {
		return false
	}
	frameCount := len(turn.frames)
	if frameCount == 0 {
		return false
	}
	ratio := turnSpeechFrameRatio(turn)
	if ratio < 0.98 {
		if frameCount <= 40 && ratio >= 0.75 && isMostlyRepeatedWord(transcript) {
			return true
		}
		return false
	}
	lowEnergy := turn.maxFrameAvg < 160 && turn.maxFramePeak < 1600
	// Clipped bursts (common when speaker echo or wake-word false trigger) should
	// not pass as valid user speech for ultra-short transcripts.
	saturated := turn.maxFrameAvg >= 8000 || turn.maxFramePeak >= 32000
	if saturated {
		if frameCount > 120 {
			return false
		}
	} else if frameCount > 24 {
		return false
	}
	if !lowEnergy && !saturated {
		return false
	}
	if isCommonHallucinatedPhrase(cleaned) {
		return true
	}
	if frameCount <= 40 && isMostlyRepeatedWord(transcript) {
		return true
	}
	if saturated {
		if isMostlyRepeatedWord(transcript) {
			return true
		}
		if turn.maxFrameAvg >= 9000 && frameCount <= 40 {
			return true
		}
		return utf8.RuneCountInString(cleaned) <= 12
	}
	// Keep this narrow: drop only very short transcript on suspicious acoustics.
	if utf8.RuneCountInString(cleaned) <= 8 {
		return true
	}
	return utf8.RuneCountInString(cleaned) <= 24
}

func shouldDropClippedWakeTurn(turn *turnBuffer) bool {
	if turn == nil {
		return false
	}
	if strings.TrimSpace(turn.wakeWord) == "" {
		return false
	}
	if len(turn.frames) == 0 || len(turn.frames) > 160 {
		return false
	}
	if turnSpeechFrameRatio(turn) < 0.9 {
		return false
	}
	if turn.maxFramePeak < 32000 {
		return false
	}
	// Strong clipping across the whole turn usually means wake-word false trigger
	// plus playback/noise leakage rather than real speech.
	return turn.maxFrameAvg >= 8000
}

func shouldDropSevereClippedNoiseTurn(turn *turnBuffer) bool {
	if turn == nil {
		return false
	}
	if strings.TrimSpace(turn.wakeWord) != "" {
		return false
	}
	// Treat fully clipped high-average turns as noise/echo leakage.
	return turn.maxFramePeak >= 32000 && turn.maxFrameAvg >= 10000
}

func shouldDropWakeNoiseTurn(session *Session, turn *turnBuffer) bool {
	if session == nil || turn == nil {
		return false
	}
	if strings.TrimSpace(turn.wakeWord) == "" {
		return false
	}
	// Always reject severely clipped wake turns: these are almost always
	// speaker leakage/noise bursts falsely decoded as wake words.
	if turn.maxFramePeak >= 32760 && turn.maxFrameAvg >= 9000 {
		return true
	}

	// During just-connected window, keep an extra conservative guard.
	elapsed := time.Since(session.joinedAt)
	if elapsed < 0 || elapsed > initialWakeWordIgnoreWindow {
		return false
	}
	return shouldDropClippedWakeTurn(turn)
}

func isMostlyRepeatedWord(text string) bool {
	parts := strings.Fields(strings.ToLower(strings.TrimSpace(text)))
	if len(parts) < 4 {
		return false
	}
	counts := make(map[string]int, len(parts))
	total := 0
	maxCount := 0
	for _, part := range parts {
		token := strings.Trim(part, ".,!?;:'\"()[]{}<>，。！？；：“”‘’")
		if token == "" {
			continue
		}
		total++
		counts[token]++
		if counts[token] > maxCount {
			maxCount = counts[token]
		}
	}
	if total < 4 {
		return false
	}
	// Treat as suspicious when one token dominates the transcript.
	return float64(maxCount)/float64(total) >= 0.7
}

func retryTranscriptWithStrongGain(ctx context.Context, stt sttStage, turn *turnBuffer) (string, error) {
	wav, err := buildStrongGainWAV(turn)
	if err != nil {
		return "", err
	}
	if len(wav) == 0 {
		return "", nil
	}
	text, err := stt.Process(ctx, wav)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func buildStrongGainWAV(turn *turnBuffer) ([]byte, error) {
	if turn == nil {
		return nil, nil
	}
	pcm, err := audio.DecodeOpusFrames(turn.frames, turn.sampleRate, turn.frameDurationMS)
	if err != nil {
		return nil, err
	}
	if len(pcm) == 0 {
		return nil, nil
	}
	inputSamples := pcm
	inputRate := turn.sampleRate
	if inputRate != 16000 {
		inputSamples = audio.ResampleLinear(inputSamples, inputRate, 16000)
		inputRate = 16000
	}
	inputSamples = applyStrongInputGain(inputSamples)
	return audio.EncodeWAV(inputRate, inputSamples)
}

func applyStrongInputGain(samples []int16) []int16 {
	if len(samples) == 0 {
		return samples
	}
	peak, avg := pcmFrameStats(samples)
	if peak < 60 || avg <= 0 {
		return samples
	}

	const (
		targetAvg = 1800.0
		maxGain   = 24.0
		minGain   = 1.15
		int16Max  = 32767.0
		int16Min  = -32768.0
	)

	gain := targetAvg / float64(avg)
	if gain < minGain {
		return samples
	}
	if gain > maxGain {
		gain = maxGain
	}

	out := make([]int16, len(samples))
	for i, sample := range samples {
		value := float64(sample) * gain
		value = math.Max(int16Min, math.Min(int16Max, value))
		out[i] = int16(value)
	}
	return out
}
