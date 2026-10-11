package promptloop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
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
	st := &promptLoopTurnState{lastAssistantID: "a1"}
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
