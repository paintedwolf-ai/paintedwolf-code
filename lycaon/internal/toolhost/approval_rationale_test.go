package toolhost_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/toolapproval"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type memCause struct {
	read briefing.CausalRead
	refs chan briefing.ActionRef
}

func (m memCause) ReadActionCause(_ context.Context, ref briefing.ActionRef) (briefing.CausalRead, error) {
	if m.refs != nil {
		m.refs <- ref
	}
	return m.read, nil
}

type memWorkers struct{ task *api.WorkerTask }

func (m memWorkers) Get(string) (*api.WorkerTask, bool) { return m.task, m.task != nil }

type memProgress struct{ content string }

func (m memProgress) Get(context.Context, string) string { return m.content }

type rootID struct{}

func (rootID) RootSessionID(_ context.Context, sessionID string) string { return sessionID }

type scriptedGenerator struct {
	mu          sync.Mutex
	out         string
	invocations []briefing.Invocation
}

func (g *scriptedGenerator) Generate(_ context.Context, invocation briefing.Invocation) (string, string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.invocations = append(g.invocations, invocation)
	return g.out, "", "", nil
}

func (g *scriptedGenerator) calls() []briefing.Invocation {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]briefing.Invocation(nil), g.invocations...)
}

// checkpointEvent is a patch (text) or a clear (empty).
type checkpointEvent struct{ text string }

type patchCapture struct{ events chan checkpointEvent }

func newPatchCapture() *patchCapture { return &patchCapture{events: make(chan checkpointEvent, 4)} }

func (p *patchCapture) next(t *testing.T) checkpointEvent {
	t.Helper()
	var expired <-chan time.Time
	if deadline, ok := t.Deadline(); ok {
		expired = time.After(time.Until(deadline) - 5*time.Second)
	}
	select {
	case event := <-p.events:
		return event
	case <-expired:
		t.Fatal("checkpoint was never patched or cleared")
		return checkpointEvent{}
	}
}

func (p *patchCapture) PatchPendingToolApprovalAIRationale(_ context.Context, _, text string) error {
	p.events <- checkpointEvent{text: text}
	return nil
}
func (p *patchCapture) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	p.events <- checkpointEvent{}
	return nil
}
func (p *patchCapture) RequestCheckpoint(context.Context, hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (p *patchCapture) PollCheckpoint(context.Context, string) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (p *patchCapture) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (p *patchCapture) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (p *patchCapture) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (p *patchCapture) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (p *patchCapture) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (p *patchCapture) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}
func (p *patchCapture) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}

func attachRequest() toolapproval.AIRationaleAttachRequest {
	return toolapproval.AIRationaleAttachRequest{
		CheckpointID: "chk",
		ToolContext:  tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "s1", WorkerJobID: "job-1", ToolCallID: "tc1"}},
		Tool:         "command", Args: map[string]any{"command": "git push"},
		Explanation: &hitl.ApprovalExplanation{What: "SUPPLIED_WHAT", Who: "SUPPLIED_WHO", IfWrong: "SUPPLIED_IF_WRONG"},
	}
}

func cause() briefing.CausalRead {
	return briefing.CausalRead{Found: true, OutputID: "out", Prose: "SUPPLIED_PROSE", IntentID: "u1", Intent: "SUPPLIED_INTENT",
		ReasoningJSON: `{"text":"SUPPLIED_REASONING"}`, ReasoningBytes: 30, CallsInOutput: 1,
		Results: []briefing.ResultExcerpt{{Tool: "read", Outcome: "success", Content: "SUPPLIED_RESULT"}}}
}

func TestApprovalRationalePatchesOneSentenceFromLiveEvidence(t *testing.T) {
	caps := newPatchCapture()
	refs := make(chan briefing.ActionRef, 1)
	generator := &scriptedGenerator{out: "Pushes the branch the user asked to ship. This advances the goal."}
	attacher := toolhost.NewApprovalRationaleAttacher(toolhost.ApprovalRationaleDeps{
		Cause: memCause{read: cause(), refs: refs}, Workers: memWorkers{task: &api.WorkerTask{Brief: "SUPPLIED_BRIEF"}},
		Progress: memProgress{content: "- [ ] SUPPLIED_STEP\n"}, Root: rootID{}, Generator: generator, Checkpoints: caps,
	})
	attacher.AttachAsync(context.Background(), attachRequest())
	if got := caps.next(t).text; got != "Pushes the branch the user asked to ship." {
		t.Fatalf("patched %q", got)
	}
	if ref := <-refs; ref != (briefing.ActionRef{SessionID: "s1", WorkerJobID: "job-1", ToolCallID: "tc1"}) {
		t.Fatalf("read anchored on %+v", ref)
	}
	calls := generator.calls()
	if len(calls) != 1 || !calls[0].Requested || calls[0].Purpose != toolhost.ApprovalProfileID {
		t.Fatalf("invocations %+v", calls)
	}
	for _, supplied := range []string{"SUPPLIED_INTENT", "SUPPLIED_PROSE", "SUPPLIED_REASONING", "SUPPLIED_RESULT", "SUPPLIED_BRIEF", "SUPPLIED_STEP", "SUPPLIED_WHAT", "git push"} {
		if !strings.Contains(calls[0].Prompt.User, supplied) {
			t.Fatalf("prompt lost %s", supplied)
		}
	}
}

func TestApprovalRationaleClearsTheSlotWhenOutputIsUnusable(t *testing.T) {
	caps := newPatchCapture()
	attacher := toolhost.NewApprovalRationaleAttacher(toolhost.ApprovalRationaleDeps{
		Cause: memCause{read: cause()}, Generator: &scriptedGenerator{out: "  **  "}, Checkpoints: caps,
	})
	attacher.AttachAsync(context.Background(), attachRequest())
	if event := caps.next(t); event.text != "" {
		t.Fatalf("unusable output patched %q", event.text)
	}
}

func TestApprovalRationaleDisabledTouchesNothing(t *testing.T) {
	caps := newPatchCapture()
	generator := &scriptedGenerator{out: "Unused."}
	attacher := toolhost.NewApprovalRationaleAttacher(toolhost.ApprovalRationaleDeps{
		Cause: memCause{read: cause()}, Generator: generator, Checkpoints: caps, EnabledFn: func() bool { return false },
	})
	if attacher.Enabled() {
		t.Fatal("Enabled ignored the setting")
	}
	attacher.AttachAsync(context.Background(), attachRequest())
	select {
	case event := <-caps.events:
		t.Fatalf("disabled attacher touched the checkpoint: %+v", event)
	default:
	}
	if len(generator.calls()) != 0 {
		t.Fatal("disabled attacher called the model")
	}
}

func TestApprovalRationaleNeedsAModel(t *testing.T) {
	if toolhost.NewApprovalRationaleAttacher(toolhost.ApprovalRationaleDeps{Cause: memCause{}, Checkpoints: newPatchCapture()}) != nil {
		t.Fatal("an attacher without a model reserves slots it cannot fill")
	}
}
