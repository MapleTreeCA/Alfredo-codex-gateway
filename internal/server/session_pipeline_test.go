package server

import (
	"context"
	"testing"
)

type synthOnlyStub struct {
	called bool
	text   string
}

func (s *synthOnlyStub) Synthesize(_ context.Context, text string) ([]byte, error) {
	s.called = true
	s.text = text
	return []byte("wav"), nil
}

type synthWithOptionsStub struct {
	called      bool
	plainCalled bool
	text        string
	options     map[string]string
}

func (s *synthWithOptionsStub) Synthesize(_ context.Context, text string) ([]byte, error) {
	s.plainCalled = true
	s.text = text
	return []byte("plain"), nil
}

func (s *synthWithOptionsStub) SynthesizeWithOptions(
	_ context.Context,
	text string,
	options map[string]string,
) ([]byte, error) {
	s.called = true
	s.text = text
	s.options = options
	return []byte("wav"), nil
}

func TestDefaultTTSStageProcessUsesOptionsWhenSupported(t *testing.T) {
	synth := &synthWithOptionsStub{}
	stage := defaultTTSStage{synthesizer: synth}

	wav, err := stage.Process(context.Background(), "hello", ttsSynthesisOptions{
		Voice: "Daniel",
		Rate:  170,
	})
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}
	if !synth.called {
		t.Fatalf("expected SynthesizeWithOptions to be called")
	}
	if synth.plainCalled {
		t.Fatalf("expected plain Synthesize not to be called")
	}
	if got := string(wav); got != "wav" {
		t.Fatalf("wav mismatch: got=%q want=%q", got, "wav")
	}
	if synth.text != "hello" {
		t.Fatalf("text mismatch: got=%q want=%q", synth.text, "hello")
	}
	if synth.options["voice"] != "Daniel" {
		t.Fatalf("voice option mismatch: got=%q want=%q", synth.options["voice"], "Daniel")
	}
	if synth.options["rate"] != "170" {
		t.Fatalf("rate option mismatch: got=%q want=%q", synth.options["rate"], "170")
	}
}

func TestDefaultTTSStageProcessFallsBackToPlainSynthesize(t *testing.T) {
	synth := &synthOnlyStub{}
	stage := defaultTTSStage{synthesizer: synth}

	wav, err := stage.Process(context.Background(), "hello", ttsSynthesisOptions{
		Voice: "Daniel",
		Rate:  170,
	})
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}
	if !synth.called {
		t.Fatalf("expected plain Synthesize to be called")
	}
	if got := string(wav); got != "wav" {
		t.Fatalf("wav mismatch: got=%q want=%q", got, "wav")
	}
	if synth.text != "hello" {
		t.Fatalf("text mismatch: got=%q want=%q", synth.text, "hello")
	}
}

type llmOnlyStub struct {
	called    bool
	sessionID string
	text      string
}

func (s *llmOnlyStub) Generate(_ context.Context, sessionID, userText string) (string, error) {
	s.called = true
	s.sessionID = sessionID
	s.text = userText
	return "ok", nil
}

type llmWithOptionsStub struct {
	called      bool
	plainCalled bool
	sessionID   string
	text        string
	options     map[string]string
}

func (s *llmWithOptionsStub) Generate(_ context.Context, sessionID, userText string) (string, error) {
	s.plainCalled = true
	s.sessionID = sessionID
	s.text = userText
	return "plain", nil
}

func (s *llmWithOptionsStub) GenerateWithOptions(
	_ context.Context,
	sessionID, userText string,
	options map[string]string,
) (string, error) {
	s.called = true
	s.sessionID = sessionID
	s.text = userText
	s.options = options
	return "ok", nil
}

func TestDefaultLLMStageProcessUsesRuntimeOptionsWhenSupported(t *testing.T) {
	llm := &llmWithOptionsStub{}
	stage := defaultLLMStage{client: llm}

	reply, err := stage.Process(context.Background(), "s1", "hello", llmGenerationOptions{
		Model:            "gpt-5.4",
		Effort:           "high",
		Verbosity:        "low",
		Online:           false,
		Concise:          false,
		ContextMessages:  7,
		MemoryRecallDays: 21,
	})
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}
	if !llm.called {
		t.Fatalf("expected GenerateWithOptions to be called")
	}
	if llm.plainCalled {
		t.Fatalf("expected plain Generate not to be called")
	}
	if reply != "ok" {
		t.Fatalf("reply mismatch: got=%q want=%q", reply, "ok")
	}
	if llm.sessionID != "s1" || llm.text != "hello" {
		t.Fatalf("input mismatch: session=%q text=%q", llm.sessionID, llm.text)
	}
	if llm.options["model"] != "gpt-5.4" {
		t.Fatalf("model mismatch: got=%q want=%q", llm.options["model"], "gpt-5.4")
	}
	if llm.options["context_messages"] != "7" {
		t.Fatalf("context_messages mismatch: got=%q want=%q", llm.options["context_messages"], "7")
	}
	if llm.options["memory_recall_days"] != "21" {
		t.Fatalf("memory_recall_days mismatch: got=%q want=%q", llm.options["memory_recall_days"], "21")
	}
	if llm.options["online"] != "false" {
		t.Fatalf("online mismatch: got=%q want=%q", llm.options["online"], "false")
	}
	if llm.options["concise"] != "false" {
		t.Fatalf("concise mismatch: got=%q want=%q", llm.options["concise"], "false")
	}
}

func TestDefaultLLMStageProcessFallsBackToPlainGenerate(t *testing.T) {
	llm := &llmOnlyStub{}
	stage := defaultLLMStage{client: llm}

	reply, err := stage.Process(context.Background(), "s1", "hello", llmGenerationOptions{
		Model:            "gpt-5.4",
		Effort:           "high",
		Verbosity:        "low",
		Online:           false,
		Concise:          true,
		ContextMessages:  7,
		MemoryRecallDays: 21,
	})
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}
	if !llm.called {
		t.Fatalf("expected Generate to be called")
	}
	if reply != "ok" {
		t.Fatalf("reply mismatch: got=%q want=%q", reply, "ok")
	}
	if llm.sessionID != "s1" || llm.text != "hello" {
		t.Fatalf("input mismatch: session=%q text=%q", llm.sessionID, llm.text)
	}
}

func TestTruncateTTSTextByMaxOutputTokens(t *testing.T) {
	short := "hello world"
	if got, truncated := truncateTTSTextByMaxOutputTokens(short, 5); truncated || got != short {
		t.Fatalf("unexpected truncation: truncated=%v got=%q want=%q", truncated, got, short)
	}

	long := "Sentence one. Sentence two. Sentence three. Sentence four. Sentence five. Sentence six."
	got, truncated := truncateTTSTextByMaxOutputTokens(long, 8)
	if !truncated {
		t.Fatalf("expected truncation")
	}
	if got == long {
		t.Fatalf("expected truncated text to differ")
	}
	if len(got) >= len(long) {
		t.Fatalf("expected shorter text: got=%d want<%d", len(got), len(long))
	}
}

func TestShouldDropLikelyHallucinatedTranscript(t *testing.T) {
	turn := &turnBuffer{
		frames:           make([][]byte, 37),
		speechFrameCount: 4,
		maxFramePeak:     484,
		maxFrameAvg:      128,
	}
	if !shouldDropLikelyHallucinatedTranscript(turn, "Thank you.") {
		t.Fatalf("expected likely hallucination to be dropped")
	}
	if !shouldDropLikelyHallucinatedTranscript(turn, "you") {
		t.Fatalf("expected short hallucinated transcript to be dropped")
	}
	if shouldDropLikelyHallucinatedTranscript(turn, "hi i am helen") {
		t.Fatalf("did not expect normal transcript to be dropped")
	}

	saturated := &turnBuffer{
		frames:           make([][]byte, 8),
		speechFrameCount: 8,
		maxFramePeak:     32767,
		maxFrameAvg:      12365,
	}
	if !shouldDropLikelyHallucinatedTranscript(saturated, "you") {
		t.Fatalf("expected saturated short burst transcript to be dropped")
	}
}

func TestShouldDropMaxTurnTimeoutTranscript(t *testing.T) {
	turn := &turnBuffer{
		frames:           make([][]byte, 198),
		speechFrameCount: 123,
		maxFramePeak:     7840,
		maxFrameAvg:      331,
		interimUpdates:   1,
	}
	if !shouldDropMaxTurnTimeoutTranscript("max_turn_timeout", turn, "I'm going to go.") {
		t.Fatalf("expected low-confidence timeout transcript to be dropped")
	}

	turn.interimUpdates = 3
	if shouldDropMaxTurnTimeoutTranscript("max_turn_timeout", turn, "I'm going to go.") {
		t.Fatalf("did not expect timeout transcript to be dropped with interim evidence")
	}

	turn.interimUpdates = 1
	if shouldDropMaxTurnTimeoutTranscript("silence_timeout", turn, "I'm going to go.") {
		t.Fatalf("did not expect silence-timeout transcript to be dropped by max-timeout rule")
	}
}

func TestShouldDropRepeatedLoopTranscript(t *testing.T) {
	turn := &turnBuffer{
		frames:           make([][]byte, 60),
		speechFrameCount: 20,
		maxFramePeak:     640,
		maxFrameAvg:      110,
		interimUpdates:   0,
	}
	raw := "I'm sorry. I'm sorry. I'm sorry. I'm sorry. I'm sorry. I'm sorry."
	collapsed := "I'm sorry."
	if !shouldDropRepeatedLoopTranscript(turn, raw, collapsed) {
		t.Fatalf("expected repeated loop transcript to be dropped")
	}

	turn.interimUpdates = 3
	turn.speechFrameCount = 50
	if shouldDropRepeatedLoopTranscript(turn, raw, collapsed) {
		t.Fatalf("did not expect drop when interim evidence and speech coverage are strong")
	}
}

func TestApplyAdaptiveInputGain(t *testing.T) {
	in := make([]int16, 1600)
	for i := range in {
		in[i] = 320
	}
	out := applyAdaptiveInputGain(in)
	inPeak, inAvg := pcmFrameStats(in)
	outPeak, outAvg := pcmFrameStats(out)
	if outAvg <= inAvg || outPeak <= inPeak {
		t.Fatalf("expected adaptive gain to boost signal: in_peak=%d out_peak=%d in_avg=%d out_avg=%d", inPeak, outPeak, inAvg, outAvg)
	}
}

func TestCollapseRepeatedSentences(t *testing.T) {
	in := "Hi, I'm Helen. Hi, I'm Helen. Hi, I'm Helen."
	got := collapseRepeatedSentences(in)
	want := "Hi, I'm Helen."
	if got != want {
		t.Fatalf("collapse mismatch: got=%q want=%q", got, want)
	}
}

func TestCollapseRepeatedSentencesKeepsDistinctContent(t *testing.T) {
	in := "Hi, I'm Helen. Nice to meet you."
	got := collapseRepeatedSentences(in)
	if got != in {
		t.Fatalf("expected unchanged transcript, got=%q want=%q", got, in)
	}
}
