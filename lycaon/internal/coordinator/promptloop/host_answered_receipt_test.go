package promptloop_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// recordingRecorder is an in-memory invocation ledger.
type recordingRecorder struct {
	mu       sync.Mutex
	begun    []invocation.Start
	settled  map[string]invocation.Settlement
	receipts map[string]*api.InvocationReceipt
}

func newRecordingRecorder() *recordingRecorder {
	return &recordingRecorder{
		settled:  map[string]invocation.Settlement{},
		receipts: map[string]*api.InvocationReceipt{},
	}
}

func (r *recordingRecorder) Begin(_ context.Context, in invocation.Start) (*api.InvocationReceipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.begun = append(r.begun, in)
	receipt := &api.InvocationReceipt{
		ID: uuid.NewString(), Tool: in.ToolName, ToolCallID: in.ToolCallID,
		Owner: in.Contract.Owner, Lifecycle: in.Contract.Lifecycle.String(),
		EvidencePolicy: string(in.Contract.Evidence()),
		Status:         api.InvocationStatusRunning,
	}
	r.receipts[receipt.ID] = receipt
	return receipt, nil
}

func (r *recordingRecorder) Settle(_ context.Context, id string, in invocation.Settlement) (*api.InvocationReceipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	receipt, ok := r.receipts[id]
	if !ok {
		return nil, errors.New("settle for an unknown receipt")
	}
	if _, already := r.settled[id]; already {
		return nil, errors.New("receipt settled twice")
	}
	r.settled[id] = in
	receipt.Status = in.Status
	receipt.Invoked = in.Invoked
	receipt.Evidence = api.InvocationEvidence{Kind: in.EvidenceKind, Ref: in.EvidenceRef, OwnerRef: in.OwnerRef}
	receipt.SourceVerdict = in.SourceVerdict
	return receipt, nil
}

func (r *recordingRecorder) ListSession(context.Context, string) ([]api.InvocationReceipt, error) {
	return nil, nil
}

func (r *recordingRecorder) ListSessionPage(context.Context, string, string, int) (api.InvocationReceiptList, error) {
	return api.InvocationReceiptList{}, nil
}

func (r *recordingRecorder) InterruptRunning(context.Context) (int64, error) { return 0, nil }

// only returns the single begun call and its settlement.
func (r *recordingRecorder) only(t *testing.T) (invocation.Start, invocation.Settlement) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.begun) != 1 {
		t.Fatalf("ledger opened %d receipts, want exactly 1", len(r.begun))
	}
	if len(r.settled) != 1 {
		t.Fatalf("ledger settled %d of 1 receipt; a receipt left running is only closed by a restart", len(r.settled))
	}
	for _, settlement := range r.settled {
		return r.begun[0], settlement
	}
	return invocation.Start{}, invocation.Settlement{}
}

// readCallDeps wires one `read` call the mock model makes, against a ledger.
func readCallDeps(t *testing.T, rec *recordingRecorder) (promptloop.PromptLoopDeps, *store.Memory) {
	t.Helper()
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		t.Fatal("subsystem owner ran: the pre-invoke hook was supposed to answer this call")
		return "", nil
	})
	mem := store.NewMemory()
	deps := promptloop.StoreDeps(mem)
	deps.Context.Limits = loopTestLimits(2)
	deps.Model.LLM = llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("review", []llm.MockToolCall{
		{ID: "tc1", Name: "read", Args: map[string]any{"path": "a.go"}},
	})})
	deps.Context.Tools = reg
	deps.Tools.Invocations = rec
	return deps, mem
}

func runReadCall(t *testing.T, deps promptloop.PromptLoopDeps, mem *store.Memory) {
	t.Helper()
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess,
		History: userHistory("review"), ProfileID: "explore_readonly",
	})
	testutil.FailErr(t, "loop.Run", err)
}

// The ledger records a hook result produced before the subsystem owner runs.
func TestHostAnsweredToolCallSettlesOnTheLedger(t *testing.T) {
	rec := newRecordingRecorder()
	deps, mem := readCallDeps(t, rec)
	deps.Tools.BeforeToolRun = func(context.Context, *api.Session, []api.Message, string, string, map[string]any) (string, bool, error) {
		return "answered by the host", true, nil
	}
	runReadCall(t, deps, mem)

	start, settlement := rec.only(t)
	if start.ToolName != "read" || start.ToolCallID != "tc1" {
		t.Fatalf("receipt opened for %s/%s, want read/tc1", start.ToolName, start.ToolCallID)
	}
	if settlement.Status != api.InvocationStatusCompleted {
		t.Fatalf("status = %q, want completed: the call did produce a result", settlement.Status)
	}
	if settlement.Invoked {
		t.Fatal("invoked = true, but no subsystem owner ran; invoked separates a host answer from an execution")
	}
	if settlement.EvidenceKind != "host_answer" {
		t.Fatalf("evidence kind = %q, want host_answer: the contract's own evidence would claim proof nobody produced",
			settlement.EvidenceKind)
	}
}

// A refusal past definition selection settles as rejected.
func TestPreInvokeRefusalSettlesOnTheLedger(t *testing.T) {
	rec := newRecordingRecorder()
	deps, mem := readCallDeps(t, rec)
	deps.Tools.BeforeToolRun = func(context.Context, *api.Session, []api.Message, string, string, map[string]any) (string, bool, error) {
		return "", false, errors.New("refused before the subsystem owner")
	}
	runReadCall(t, deps, mem)

	_, settlement := rec.only(t)
	if settlement.Status != api.InvocationStatusRejected {
		t.Fatalf("status = %q, want rejected", settlement.Status)
	}
	if settlement.Invoked {
		t.Fatal("invoked = true, but the hook refused before the subsystem owner")
	}
	if settlement.Failure == nil {
		t.Fatal("a non-completed receipt carries no failure; the ledger cannot say why it ended")
	}
}

// Refusal before definition selection opens no receipt.
func TestOffSurfaceToolOpensNoReceipt(t *testing.T) {
	rec := newRecordingRecorder()
	reg := tools.NewStubRegistry()
	mem := store.NewMemory()
	deps := promptloop.StoreDeps(mem)
	deps.Context.Limits = loopTestLimits(2)
	deps.Model.LLM = llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("review", []llm.MockToolCall{
		{ID: "tc1", Name: "no_such_tool", Args: map[string]any{}},
	})})
	deps.Context.Tools = reg
	deps.Tools.Invocations = rec
	runReadCall(t, deps, mem)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.begun) != 0 {
		t.Fatalf("ledger opened %d receipts for a tool with no definition, want 0", len(rec.begun))
	}
}

// The ledger settles the subsystem owner's source verdict.
func TestStatedSourceVerdictReachesTheLedger(t *testing.T) {
	rec := newRecordingRecorder()
	reg := tools.NewStubRegistry()
	_ = reg.Register("command", func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
		if tctx.Out != nil {
			tctx.Out.SourceRun = &tools.SourceRunCapture{
				Command: "./check.sh", ExitCode: 0, Verdict: api.SourceVerdictPassed,
			}
		}
		return `{"ok":true,"exit_code":0}`, nil
	})
	mem := store.NewMemory()
	deps := promptloop.StoreDeps(mem)
	deps.Context.Limits = loopTestLimits(2)
	deps.Model.LLM = llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("review", []llm.MockToolCall{
		{ID: "tc1", Name: "command", Args: map[string]any{"command": "./check.sh"}},
	})})
	deps.Context.Tools = reg
	deps.Tools.Invocations = rec
	runReadCall(t, deps, mem)

	_, settlement := rec.only(t)
	if settlement.Status != api.InvocationStatusCompleted {
		t.Fatalf("status = %q, want completed", settlement.Status)
	}
	if settlement.SourceVerdict != api.SourceVerdictPassed {
		t.Fatalf("settled verdict = %q, want %q", settlement.SourceVerdict, api.SourceVerdictPassed)
	}
}

// A subsystem owner that ran no source states no verdict.
func TestOwnerWithoutSourceRunSettlesNoVerdict(t *testing.T) {
	rec := newRecordingRecorder()
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "file contents", nil
	})
	mem := store.NewMemory()
	deps := promptloop.StoreDeps(mem)
	deps.Context.Limits = loopTestLimits(2)
	deps.Model.LLM = llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("review", []llm.MockToolCall{
		{ID: "tc1", Name: "read", Args: map[string]any{"path": "a.go"}},
	})})
	deps.Context.Tools = reg
	deps.Tools.Invocations = rec
	runReadCall(t, deps, mem)

	_, settlement := rec.only(t)
	if settlement.SourceVerdict != "" {
		t.Fatalf("settled verdict = %q for a subsystem owner that ran no source", settlement.SourceVerdict)
	}
}

// A coded refusal is routed into the next turn, so the batch writes its card.
// The settled ledger row travels with it; worker completion proof reads
// receipts off transcript rows.
func TestGuidanceRejectedCallCarriesItsReceiptToTheCard(t *testing.T) {
	rec := newRecordingRecorder()
	deps, mem := readCallDeps(t, rec)
	deps.Tools.BeforeToolRun = func(context.Context, *api.Session, []api.Message, string, string, map[string]any) (string, bool, error) {
		return "", false, guidance.NewRefusal("TOOL_TEST_REFUSED", "Rejected: no.\nCode: TOOL_TEST_REFUSED")
	}

	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess,
		History: userHistory("review"), ProfileID: "explore_readonly",
	})
	testutil.FailErr(t, "loop.Run", err)

	_, settlement := rec.only(t)
	if settlement.Status != api.InvocationStatusRejected {
		t.Fatalf("status = %q, want rejected", settlement.Status)
	}

	msgs, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read transcript", err)
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if msg.ToolResult.ToolCallID != "tc1" {
			continue
		}
		if msg.ToolResult.Invocation == nil {
			t.Fatal("the reject card names no invocation; its settled ledger row is unreachable from the transcript")
		}
		if msg.ToolResult.Invocation.Status != api.InvocationStatusRejected {
			t.Fatalf("card receipt status = %q, want rejected", msg.ToolResult.Invocation.Status)
		}
		return
	}
	t.Fatal("no tool card was written for the refused call")
}
