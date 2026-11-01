package promptloop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type softStopSequenceLLM struct {
	completions  []*modelcall.Completion
	index        int
	requestTools [][]string
}

func (s *softStopSequenceLLM) Complete(ctx context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.index >= len(s.completions) {
		return nil, fmt.Errorf("unexpected model turn %d", s.index+1)
	}
	out := *s.completions[s.index]
	s.index++
	return &out, nil
}

func (s *softStopSequenceLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	names := make([]string, 0, len(req.Tools))
	for _, meta := range req.Tools {
		names = append(names, meta.Name)
	}
	s.requestTools = append(s.requestTools, names)
	ch := make(chan modelcall.StreamChunk, 1)
	completion, err := s.Complete(ctx, req)
	if err != nil {
		close(ch)
		return ch, err
	}
	ch <- modelcall.StreamChunk{Content: completion.Content, ToolCalls: completion.ToolCalls, Done: true}
	close(ch)
	return ch, nil
}

type softStopToolPolicy struct{}

func (softStopToolPolicy) ListForPrompt(context.Context, *api.Session, string) []tools.ToolMeta {
	return []tools.ToolMeta{{Name: "read"}, {Name: "task"}}
}

func (softStopToolPolicy) EvaluateInvoke(context.Context, *api.Session, string, map[string]any) error {
	return nil
}

func TestMaybeSpendRunwayNudge(t *testing.T) {
	var calls int
	var gotCeiling float64
	loop := NewPromptLoop(PromptLoopDeps{
		Nudges: NudgesDeps{
			SpendRunwayNudge: func(_ context.Context, _ *api.Session, ceilingUSD float64) HostNudge {
				calls++
				gotCeiling = ceilingUSD
				return HostNudge{Content: "spend runway heads-up"}
			},
		},
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error {
				return nil
			},
		},
	})
	sess := &api.Session{ID: "s1"}
	st := &promptLoopTurnState{}
	history := []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "hi"}}

	got, err := loop.Nudges.maybeSpendRunwayNudge(context.Background(), sess, "s1", history, SpendRunway{Low: true, CeilingUSD: 5}, st)
	testutil.FailErr(t, "turnNudges{loop}.maybeSpendRunwayNudge failed", err)
	if calls != 1 || gotCeiling != 5 {
		t.Fatalf("calls=%d ceiling=%v", calls, gotCeiling)
	}
	if len(got) != 2 {
		t.Fatalf("history len = %d want 2", len(got))
	}

	got2, err := loop.Nudges.maybeSpendRunwayNudge(context.Background(), sess, "s1", got, SpendRunway{Low: false, CeilingUSD: 5}, st)
	testutil.FailErr(t, "turnNudges{loop}.maybeSpendRunwayNudge failed", err)
	if calls != 1 {
		t.Fatalf("Low=false called nudge: %d", calls)
	}
	if len(got2) != 2 {
		t.Fatalf("history grew when Low=false")
	}
}

func TestApplySpendCeilingGrantsOneCoordinatorWindDown(t *testing.T) {
	ceilingErr := errors.New("ceiling reached")
	loop := NewPromptLoop(PromptLoopDeps{
		Nudges: NudgesDeps{
			CheckSpendCeiling: func(context.Context, string, *api.Session) (SpendCeilingCheck, error) {
				return SpendCeilingCheck{SoftStop: true}, ceilingErr
			},
			IsSpendCeiling: func(err error) bool { return errors.Is(err, ceilingErr) },
		},
	})
	st := &promptLoopTurnState{turnStreamState: turnStreamState{lastAssistantID: "a1"}}
	decision, err := loop.Nudges.applySpendCeiling(
		context.Background(), &api.Session{ID: "s1"}, "s1", "implement", "prompt", 10,
		PromptRunInput{}, st,
	)
	if err != nil {
		t.Fatalf("applySpendCeiling: %v", err)
	}
	if !decision.WindDown || !st.spendSoftStopGranted {
		t.Fatalf("decision=%+v state=%+v", decision, st)
	}
}

func TestMaybeSpendSoftStopNudge(t *testing.T) {
	var appended []api.Message
	loop := NewPromptLoop(PromptLoopDeps{
		Nudges: NudgesDeps{
			SpendSoftStopNudge: func(context.Context, *api.Session) HostNudge {
				return HostNudge{Content: "bounded landing"}
			},
		},
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				appended = append(appended, msgs...)
				return nil
			},
		},
	})
	history := []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "hi"}}
	got, err := loop.Nudges.maybeSpendSoftStopNudge(
		context.Background(), &api.Session{ID: "s1"}, "s1", history, true, &promptLoopTurnState{},
	)
	if err != nil {
		t.Fatalf("maybeSpendSoftStopNudge: %v", err)
	}
	if len(got) != 2 || len(appended) != 1 || appended[0].Visibility != api.MessageVisibilityInternal {
		t.Fatalf("history=%+v appended=%+v", got, appended)
	}
}

func TestSoftStopDropsWorkerDelegation(t *testing.T) {
	calls := []api.ToolCall{{Name: "task", ID: "t1"}, {Name: "command", ID: "b1"}}
	got := filterSpendSoftStopToolCalls(calls)
	if len(got) != 1 || got[0].Name != "command" {
		t.Fatalf("filtered calls = %+v", got)
	}
	metas := filterSpendSoftStopToolMetas([]tools.ToolMeta{{Name: "task"}, {Name: "read"}})
	if len(metas) != 1 || metas[0].Name != "read" {
		t.Fatalf("filtered metadata = %+v", metas)
	}
}

func TestPromptLoopSoftStopAllowsOneToolRoundThenClosesOut(t *testing.T) {
	ceilingErr := errors.New("ceiling reached")
	client := &softStopSequenceLLM{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{{Name: "read", ID: "before", Args: map[string]any{"path": "a"}}}},
		{ToolCalls: []api.ToolCall{{Name: "read", ID: "landing", Args: map[string]any{"path": "b"}}}},
		{Content: "Landed with the best usable result."},
	}}
	messages := store.NewMemory()
	deps := StoreDeps(messages)
	deps.Model.LLM = client
	deps.Context.Policy = softStopToolPolicy{}
	deps.Context.Tools = tools.NewStubRegistry()
	var checks int
	deps.Nudges.CheckSpendCeiling = func(context.Context, string, *api.Session) (SpendCeilingCheck, error) {
		checks++
		if checks == 1 {
			return SpendCeilingCheck{}, nil
		}
		return SpendCeilingCheck{SoftStop: true}, ceilingErr
	}
	deps.Nudges.IsSpendCeiling = func(err error) bool { return errors.Is(err, ceilingErr) }
	deps.Nudges.SpendSoftStopNudge = func(context.Context, *api.Session) HostNudge {
		return HostNudge{Content: "Use one bounded wind-down round."}
	}
	deps.Closeout.TurnCloseoutNudge = func(context.Context, *api.Session, string, TurnCloseoutCause) HostNudge {
		return HostNudge{Content: "Give the final prose closeout."}
	}
	var admitted int
	deps.Projection.AdmitModelResponse = func(_ context.Context, _ string, attemptID string) error {
		if attemptID != "attempt" {
			t.Fatalf("admitted attempt = %q", attemptID)
		}
		admitted++
		return nil
	}
	loop := NewPromptLoopForTest(deps)
	sess, err := messages.Create(
		context.Background(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "project",
	)
	testutil.FailErr(t, "create session", err)
	result, err := loop.Run(context.Background(), PromptRunInput{
		TurnID: "turn", AttemptID: "attempt",
		SessionID: sess.ID,
		Session:   sess,
		History:   []api.Message{{Role: api.MessageRoleUser, Content: "finish it"}},
		ProfileID: "implement",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "run prompt loop", err)
	if admitted != 3 || result.Closeout == nil || result.Closeout.ID != result.LastAssistantID || result.Closeout.Content != result.LastAssistantContent {
		t.Fatalf("execution admission=%d sealed=%+v result=%+v", admitted, result.Closeout, result)
	}
	if checks != 3 || client.index != 3 {
		t.Fatalf("checks=%d model turns=%d want 3/3", checks, client.index)
	}
	if len(client.requestTools) != 3 || slices.Contains(client.requestTools[1], "task") {
		t.Fatalf("wind-down advertised tools = %v", client.requestTools)
	}
	if result.LastAssistantContent != "Landed with the best usable result." {
		t.Fatalf("content = %q", result.LastAssistantContent)
	}
}

func TestContextOverflow400TriggersCompactionAndOneRetry(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			call := attempts.Add(1)
			if call == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"message":"request (33442 tokens) exceeds the available context size (32768 tokens), try increasing it","type":"exceed_context_size_error","n_prompt_tokens":33442,"n_ctx":32768}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"recovered after compaction"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4096,"completion_tokens":12,"total_tokens":4108}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	provider := openaicompat.New("openai-compatible", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "qwen2.5-coder"}}).
		WithProfile(providerprofile.LocalInference(providerprofile.OpenAI()))

	var loweredWindow int
	var compactCalled int
	var observedWindow int
	var observedPrompts []int
	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			LLM:              provider,
			CompactionConfig: staticCompactionConfig,
			LowerSessionLiveWindow: func(_ string, window int) {
				loweredWindow = window
			},
			CompactAfterContextOverflow: func(_ context.Context, _ *api.Session, history []api.Message, reportedWindow int) ([]api.Message, error) {
				compactCalled++
				observedWindow = reportedWindow
				if len(history) > 2 {
					return history[len(history)-2:], nil
				}
				return history, nil
			},
			RecordCompactionTokenObservation: func(_, _, _ string, reportedPromptTokens, _ int) {
				observedPrompts = append(observedPrompts, reportedPromptTokens)
			},
		},
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error {
				return nil
			},
		},
		Context: ContextDeps{
			Policy: &recordingToolPolicy{},
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
		},
	})

	sess := &api.Session{
		ID:            "s-overflow",
		ProviderID:    "openai-compatible",
		Model:         "qwen2.5-coder",
		WorkspacePath: t.TempDir(),
	}
	initialHistory := []api.Message{
		{ID: "m1", Role: api.MessageRoleUser, Content: strings.Repeat("earlier context ", 500)},
		{ID: "m2", Role: api.MessageRoleAssistant, Content: strings.Repeat("earlier reply ", 500)},
		{ID: "m3", Role: api.MessageRoleUser, Content: "please summarize"},
	}
	st := &promptLoopTurnState{history: append([]api.Message(nil), initialHistory...)}

	completion, _, err := loop.Model.completeStream(
		context.Background(),
		sess,
		sess.ID,
		initialHistory,
		"coordinator",
		"please summarize",
		0,
		4,
		false,
		st,
		nil,
	)
	if err != nil {
		t.Fatalf("completeStream failed after overflow recovery: %v", err)
	}
	if completion == nil || completion.Content != "recovered after compaction" {
		t.Fatalf("completion = %+v; want 'recovered after compaction'", completion)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("chat completion attempts = %d want 2 (initial + 1 retry)", got)
	}
	if compactCalled != 1 {
		t.Fatalf("CompactAfterContextOverflow calls = %d want 1", compactCalled)
	}
	if loweredWindow != 32768 || observedWindow != 32768 {
		t.Fatalf("loweredWindow=%d observedWindow=%d; want 32768", loweredWindow, observedWindow)
	}
	if len(observedPrompts) != 2 || observedPrompts[0] != 33442 || observedPrompts[1] != 4096 {
		t.Fatalf("observedPrompts = %v want [33442 4096]", observedPrompts)
	}
	effCfg, ok := loop.Model.effectiveCompactionConfig(context.Background(), sess)
	if !ok || effCfg.ModelContextWindow != 32768 {
		t.Fatalf("effectiveCompactionConfig.ModelContextWindow = %d (ok=%v); want 32768", effCfg.ModelContextWindow, ok)
	}
	entry, ok := provider.ModelEntry("qwen2.5-coder")
	if !ok || entry.ContextLength != 32768 {
		t.Fatalf("provider ModelEntry = %+v, ok=%v; want ContextLength=32768", entry, ok)
	}
}

func TestContextOverflow400RetryFailsTurnAfterSingleRetry(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 16384 tokens. However, your messages resulted in 20000 tokens.","type":"invalid_request_error","code":"context_length_exceeded"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	provider := openaicompat.New("openai-compatible", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "qwen2.5-coder"}}).
		WithProfile(providerprofile.LocalInference(providerprofile.OpenAI()))

	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			LLM:              provider,
			CompactionConfig: func(context.Context, *api.Session) compaction.CompactionConfig { return compaction.DefaultCompactionConfig() },
		},
		Context: ContextDeps{
			Policy: &recordingToolPolicy{},
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
		},
	})
	sess := &api.Session{ID: "s-overflow-fail", ProviderID: "openai-compatible", Model: "qwen2.5-coder", WorkspacePath: t.TempDir()}
	st := &promptLoopTurnState{}
	_, _, err := loop.Model.completeStream(
		context.Background(),
		sess,
		sess.ID,
		[]api.Message{{ID: "m1", Role: api.MessageRoleUser, Content: "hello"}},
		"coordinator",
		"hello",
		0,
		4,
		false,
		st,
		nil,
	)
	if err == nil {
		t.Fatal("expected error when retry also overflows")
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d want 2 (1 initial + 1 retry)", got)
	}
	tooSmall, ok := failure.AsProviderContextTooSmall(err)
	if !ok || tooSmall.MaxContext != 16384 {
		t.Fatalf("expected ProviderContextTooSmallError with MaxContext=16384, got %T: %v", err, err)
	}
}
