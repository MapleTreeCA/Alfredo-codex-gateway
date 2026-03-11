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
	transcript, err := p.stt.Process(ctx, ingress.wav)
	if err != nil {
		_ = session.sendAlert("error", truncateForSpeech(err.Error()), "sad")
		log.Printf("session=%s turn=%d stt failed: %v", session.id, turn.id, err)
		return
	}
	rawTranscript := transcript
	if shouldRetryWeakTranscript(turn, transcript) {
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
	log.Printf(
		"session=%s turn=%d stt ok duration=%s text=%q",
		session.id,
		turn.id,
		time.Since(stageStartedAt).Round(time.Millisecond),
		ellipsizeLogText(transcript, 120),
	)
	if err := session.sendPipelineJSON(ctx, turn.id, map[string]any{
		"session_id": session.id,
		"type":       "stt",
		"state":      "final",
		"text":       transcript,
	}); err != nil {
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

	stageStartedAt = time.Now()
	ttsOptions := ttsSynthesisOptions{
		Voice: runtime.TTSVoice,
		Rate:  runtime.TTSRate,
	}
	speechWAV, err := p.tts.Process(ctx, spokenText, ttsOptions)
	if err != nil {
		_ = session.sendAlert("error", truncateForSpeech(err.Error()), "sad")
		log.Printf("session=%s turn=%d tts failed: %v", session.id, turn.id, err)
		return
	}
	log.Printf(
		"session=%s turn=%d tts ok duration=%s wav_bytes=%d voice=%q rate=%d",
		session.id,
		turn.id,
		time.Since(stageStartedAt).Round(time.Millisecond),
		len(speechWAV),
		ttsOptions.Voice,
		ttsOptions.Rate,
	)

	downlink, err := p.downlink.Process(
		speechWAV,
		session.downlinkSampleRate,
		session.server.cfg.OpusFrameDuration,
		session.server.cfg.DownlinkOpusBitrate,
	)
	if err != nil {
		_ = session.sendAlert("error", truncateForSpeech(err.Error()), "sad")
		log.Printf("session=%s turn=%d downlink failed: %v", session.id, turn.id, err)
		return
	}
	log.Printf(
		"session=%s turn=%d downlink opus_frames=%d sample_rate=%d bitrate=%d",
		session.id,
		turn.id,
		len(downlink.frames),
		downlink.sampleRate,
		session.server.cfg.DownlinkOpusBitrate,
	)

	if maxDuration := session.server.cfg.TTSMaxDuration; maxDuration > 0 && session.server.cfg.OpusFrameDuration > 0 {
		frameStep := time.Duration(session.server.cfg.OpusFrameDuration) * time.Millisecond
		maxFrames := int(maxDuration / frameStep)
		if maxDuration%frameStep != 0 {
			maxFrames++
		}
		if maxFrames < 1 {
			maxFrames = 1
		}
		if len(downlink.frames) > maxFrames {
			originalFrames := len(downlink.frames)
			downlink.frames = downlink.frames[:maxFrames]
			log.Printf(
				"session=%s turn=%d tts downlink truncated max_duration=%s frame_ms=%d original_frames=%d sent_frames=%d",
				session.id,
				turn.id,
				maxDuration.Round(time.Millisecond),
				session.server.cfg.OpusFrameDuration,
				originalFrames,
				len(downlink.frames),
			)
		}
	}

	for i, frame := range downlink.frames {
		if err := session.sendBinary(ctx, turn.id, frame); err != nil {
			return
		}
		// Stream frames at real-time pace to avoid overflowing device playback
		// queues when replies are long (for example, slower TTS rates).
		if i < len(downlink.frames)-1 && session.server.cfg.OpusFrameDuration > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(session.server.cfg.OpusFrameDuration) * time.Millisecond):
			}
		}
	}

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
