package capabilitydrift

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/capabilitydrift/fixture"
	"github.com/lycaon/lycaon/test/contract/internal/rejectinventory"
)

// recipientProfile is the worker profile every fixture request is built for.
const recipientProfile = "worker_readonly"

// turn is one isolated request: the frozen host state, the projection that
// builds its delivery, and the calls the scripted model answers with.
type turn struct {
	state      fixture.HostState
	projection fixture.Projection
	calls      []api.ToolCall
	// extra registers further definitions beside the fixture handler.
	extra []tools.Definition
	// decline makes the generic boundary ask for approval, which the person declines.
	decline bool
}

// turnResult is what the production prompt loop did with the turn.
type turnResult struct {
	requests []modelcall.CompletionRequest
	begun    []invocation.Start
	receipts []api.InvocationReceipt
}

func runTurn(t *testing.T, tr turn) turnResult {
	t.Helper()
	reg := tools.NewDefaultRegistry()
	for _, def := range append([]tools.Definition{fixture.Definition(tr.state)}, tr.extra...) {
		if err := reg.RegisterDefinition(def); err != nil {
			t.Fatalf("register %s: %v", def.Meta.Name, err)
		}
	}
	exec := toolexecution.NewExecutor(boundaryPolicy{ask: tr.decline}, reg, recipientProfile)
	if tr.decline {
		exec.Approvals.SetCheckpointManager(t.Context(), decliningCheckpoints{}, nil)
	}
	delivery := tr.projection(tr.state)
	metas := slices.Clone(delivery.Tools)
	for _, def := range tr.extra {
		metas = append(metas, def.Meta)
	}
	model := &scriptedModel{calls: tr.calls}
	receipts := &receiptLog{receipts: map[string]*api.InvocationReceipt{}}
	sess := &api.Session{ID: "drift-" + tr.state.ID, AgentType: recipientProfile, Posture: api.SessionPostureBuild, WorkspacePath: t.TempDir()}

	loop := promptloop.NewPromptLoop(promptloop.PromptLoopDeps{
		Model: promptloop.ModelDeps{LLM: model},
		Context: promptloop.ContextDeps{
			Tools:  tools.NewExecutorRegistry(exec, reg),
			Policy: projectedPolicy{metas: metas},
			ResolveEffectiveMode: func(_ context.Context, id string) (store.RootAuthority, error) {
				return store.RootAuthority{RootSessionID: id, Mode: api.SessionModeAgent, Started: true}, nil
			},
			Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				if delivery.Instruction == "" {
					return history, nil
				}
				instruction := api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Content: delivery.Instruction}
				return append([]api.Message{instruction}, history...), nil
			},
			LoadedTools:      func(string) map[string]bool { return map[string]bool{} },
			ProjectRootCount: func(context.Context, *api.Session) int { return 1 },
		},
		Tools: promptloop.ToolsDeps{Invocations: receipts},
		Projection: promptloop.ProjectionDeps{
			AppendMessages:              func(context.Context, string, ...api.Message) error { return nil },
			UpdateMessage:               func(context.Context, string, string, api.Message) error { return nil },
			AppendDraftVersion:          func(context.Context, string, string, string, string) (int, error) { return 1, nil },
			CountDraftVersions:          func(context.Context, string, string) (int, error) { return 0, nil },
			SettledModelOutputIteration: func(context.Context, string) (int, error) { return 0, nil },
		},
		Closeout: promptloop.CloseoutDeps{
			EvidenceLedger: emptyLedger{},
			EvaluateCloseoutBlock: func(context.Context, *api.Session, *oar.GuardContext) (*oar.Decision, error) {
				return nil, nil
			},
		},
	})
	history := []api.Message{{Role: api.MessageRoleUser, Content: "Handle the weekly report."}}
	if _, err := loop.Run(t.Context(), promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess, History: history, ProfileID: recipientProfile,
		ToolCtx: tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID, ProjectID: "drift-project", Agent: recipientProfile}},
	}); err != nil {
		t.Fatalf("prompt loop for %s: %v", tr.state.ID, err)
	}
	return turnResult{requests: model.captured(), begun: receipts.started(), receipts: receipts.settled()}
}

// witnessFrom records the settled receipt of the fixture call together with
// the owner's typed admission decision for the same state and arguments.
func witnessFrom(state fixture.HostState, res turnResult) (rejectinventory.Witness, bool) {
	for _, r := range res.receipts {
		if r.Tool != fixture.ToolName || r.Failure == nil {
			continue
		}
		if fixture.Admit(state, fixture.Args()).Admitted {
			return rejectinventory.Witness{}, false
		}
		w := rejectinventory.Witness{
			Tool: r.Tool, Recipient: state.Recipient, State: state.ID,
			Decision: rejectinventory.DecisionIdentity(fixture.Admit), Inputs: fixture.Args(),
			Code: r.Failure.Code, FailureClass: r.Failure.Class, Status: string(r.Status), Invoked: r.Invoked,
		}
		if r.Isolation != nil {
			w.Disposition = r.Isolation.Disposition
		}
		return w, true
	}
	return rejectinventory.Witness{}, false
}

// boundaryPolicy admits every call, or asks for approval when ask is set.
type boundaryPolicy struct{ ask bool }

func (p boundaryPolicy) Evaluate(context.Context, platform.PolicyContext) (*platform.PolicyDecision, error) {
	return &platform.PolicyDecision{Allowed: true, RequiresApproval: p.ask}, nil
}

// projectedPolicy lists the tools the projection put on the request.
type projectedPolicy struct{ metas []tools.ToolMeta }

func (p projectedPolicy) ListForPrompt(context.Context, *api.Session, string) []tools.ToolMeta {
	return slices.Clone(p.metas)
}

func (projectedPolicy) EvaluateInvoke(context.Context, *api.Session, string, map[string]any) error {
	return nil
}

// scriptedModel answers the first request with its calls and every later one
// with prose, capturing each request.
type scriptedModel struct {
	mu       sync.Mutex
	calls    []api.ToolCall
	requests []modelcall.CompletionRequest
}

func (m *scriptedModel) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	if len(m.requests) == 1 && len(m.calls) > 0 {
		return &modelcall.Completion{ToolCalls: slices.Clone(m.calls)}, nil
	}
	return &modelcall.Completion{Content: "Done."}, nil
}

func (m *scriptedModel) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	completion, err := m.Complete(ctx, req)
	ch := make(chan modelcall.StreamChunk, 1)
	if err == nil {
		ch <- modelcall.StreamChunk{Content: completion.Content, ToolCalls: completion.ToolCalls, Done: true}
	}
	close(ch)
	return ch, err
}

func (m *scriptedModel) captured() []modelcall.CompletionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.requests)
}

// receiptLog is an in-memory receipt recorder.
type receiptLog struct {
	mu       sync.Mutex
	begun    []invocation.Start
	order    []string
	receipts map[string]*api.InvocationReceipt
}

func (l *receiptLog) Begin(_ context.Context, in invocation.Start) (*api.InvocationReceipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := fmt.Sprintf("receipt-%d", len(l.order)+1)
	r := &api.InvocationReceipt{ID: id, Tool: in.ToolName, ToolCallID: in.ToolCallID, Owner: in.Contract.Owner,
		Status: api.InvocationStatusRunning, StartedAt: time.Now().UTC()}
	l.begun = append(l.begun, in)
	l.order = append(l.order, id)
	l.receipts[id] = r
	clone := *r
	return &clone, nil
}

func (l *receiptLog) Settle(_ context.Context, id string, s invocation.Settlement) (*api.InvocationReceipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.receipts[id]
	if !ok {
		return nil, fmt.Errorf("unknown receipt %s", id)
	}
	if r.SettledAt != nil {
		return nil, fmt.Errorf("receipt %s settled twice", id)
	}
	now := time.Now().UTC()
	r.Status, r.Invoked, r.Failure, r.Isolation, r.SettledAt = s.Status, s.Invoked, s.Failure, s.Isolation, &now
	clone := *r
	return &clone, nil
}

func (l *receiptLog) ListSession(context.Context, string) ([]api.InvocationReceipt, error) {
	return l.settled(), nil
}

func (l *receiptLog) ListSessionPage(context.Context, string, string, int) (api.InvocationReceiptList, error) {
	return api.InvocationReceiptList{}, nil
}

func (l *receiptLog) InterruptRunning(context.Context) (int64, error) { return 0, nil }

func (l *receiptLog) started() []invocation.Start {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.begun)
}

func (l *receiptLog) settled() []api.InvocationReceipt {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]api.InvocationReceipt, 0, len(l.order))
	for _, id := range l.order {
		out = append(out, *l.receipts[id])
	}
	return out
}

type emptyLedger struct{}

func (emptyLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, nil
}

func (emptyLedger) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

// decliningCheckpoints is a person who declines every approval card.
type decliningCheckpoints struct{}

func (decliningCheckpoints) RequestCheckpoint(context.Context, hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{CheckpointID: "chk-decline", Status: hitl.DecisionStatusPending}, nil
}

func (decliningCheckpoints) PollCheckpoint(_ context.Context, id string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{CheckpointID: id, Status: hitl.DecisionStatusRejected, Result: &hitl.DecisionResult{Approved: false}}, nil
}

func (decliningCheckpoints) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}

func (decliningCheckpoints) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}

func (decliningCheckpoints) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}

func (decliningCheckpoints) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}

func (decliningCheckpoints) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}

func (decliningCheckpoints) SessionApprovalDenied(context.Context, string) (bool, error) {
	return true, nil
}

func (decliningCheckpoints) PatchPendingToolApprovalAIRationale(context.Context, string, string, string) (bool, error) {
	return false, nil
}

func (decliningCheckpoints) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}

func (decliningCheckpoints) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}
