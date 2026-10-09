package toolhost_test

import (
	"github.com/lycaon/lycaon/internal/toolapproval"

	"context"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type memMessages struct {
	msgs []api.Message
}

func (m memMessages) GetMessages(context.Context, string) ([]api.Message, error) {
	return m.msgs, nil
}

type memWorkers struct {
	task *api.WorkerTask
}

func (m memWorkers) Get(string) (*api.WorkerTask, bool) {
	if m.task == nil {
		return nil, false
	}
	return m.task, true
}

type memProgress struct {
	content string
}

func (m memProgress) Get(context.Context, string) string { return m.content }

type rootID struct{}

func (rootID) RootSessionID(_ context.Context, sessionID string) string { return sessionID }

type patchCapture struct {
	mu     sync.Mutex
	calls  []string
	clears int
	events int
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
func (p *patchCapture) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (p *patchCapture) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (p *patchCapture) PatchPendingToolApprovalAIRationale(_ context.Context, _, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, text)
	p.events++
	return nil
}
func (p *patchCapture) ClearPendingToolApprovalAIRationale(_ context.Context, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clears++
	p.events++
	return nil
}
func (p *patchCapture) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

type recordingSummarizer struct {
	mu     sync.Mutex
	called bool
	out    string
}

func (r *recordingSummarizer) Summarize(context.Context, string, string, int) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called = true
	if r.out == "" {
		return "Advances the current task.", nil
	}
	return r.out, nil
}

func (r *recordingSummarizer) wasCalled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called
}

// scriptedSummarizer answers each Summarize call from a queue and records the
// system prompt it was asked with, so call count is observable.
type scriptedSummarizer struct {
	mu      sync.Mutex
	outs    []string
	systems []string
}

func (s *scriptedSummarizer) Summarize(_ context.Context, system, _ string, _ int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.systems = append(s.systems, system)
	if len(s.outs) == 0 {
		return "", nil
	}
	out := s.outs[0]
	s.outs = s.outs[1:]
	return out, nil
}

func (s *scriptedSummarizer) systemPrompts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.systems...)
}

func rationaleAttacher(t *testing.T, sum compaction.Summarizer, caps *patchCapture) toolapproval.AIRationaleAttacher {
	t.Helper()
	return toolhost.NewApprovalRationaleAttacher(toolhost.ApprovalRationaleDeps{
		Messages: memMessages{msgs: []api.Message{
			{Role: api.MessageRoleUser, Content: "git push"},
			{Role: api.MessageRoleAssistant, Content: "Pushing.", ToolCalls: []api.ToolCall{{ID: "tc1", Name: "command"}}},
		}},
		Workers:     memWorkers{},
		Progress:    memProgress{},
		Root:        rootID{},
		Summarizer:  sum,
		Checkpoints: caps,
	})
}

func awaitPatch(t *testing.T, caps *patchCapture) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		caps.mu.Lock()
		n := len(caps.calls)
		text := ""
		if n > 0 {
			text = caps.calls[0]
		}
		caps.mu.Unlock()
		if n > 0 {
			return text
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a rationale patch")
	return ""
}

// An over-long answer is clipped to its first sentence, on one summarize call.
func TestApprovalRationale_clipsToOneSentence(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")
	caps := &patchCapture{}
	sum := &scriptedSummarizer{outs: []string{
		"Pushes the branch the user asked to ship. This advances the goal by publishing the reviewed work to the remote.",
	}}
	rationaleAttacher(t, sum, caps).AttachAsync(context.Background(), toolapproval.AIRationaleAttachRequest{
		CheckpointID: "chk-clip",
		ToolContext:  tools.ToolContext{SessionID: "s1", ToolCallID: "tc1"},
		Tool:         "command",
		Args:         map[string]any{"command": "git push"},
	})
	if got := awaitPatch(t, caps); got != "Pushes the branch the user asked to ship." {
		t.Fatalf("got %q", got)
	}
	if n := len(sum.systemPrompts()); n != 1 {
		t.Fatalf("the card note costs exactly one call, got %d", n)
	}
}

// Every non-egress tool_approval card gets a rationale attempt.
func TestApprovalRationale_attemptsOneShot(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")
	caps := &patchCapture{}
	sum := &recordingSummarizer{out: "Pushes the branch the user asked to ship."}
	attacher := toolhost.NewApprovalRationaleAttacher(toolhost.ApprovalRationaleDeps{
		Messages: memMessages{msgs: []api.Message{
			{Role: api.MessageRoleUser, Content: "git push"},
			{Role: api.MessageRoleAssistant, Content: "Pushing.", ToolCalls: []api.ToolCall{{ID: "tc1", Name: "command"}}},
		}},
		Workers:     memWorkers{},
		Progress:    memProgress{},
		Root:        rootID{},
		Summarizer:  sum,
		Checkpoints: caps,
	})
	attacher.AttachAsync(context.Background(), toolapproval.AIRationaleAttachRequest{
		CheckpointID: "chk-1",
		ToolContext:  tools.ToolContext{SessionID: "s1", ToolCallID: "tc1"},
		Tool:         "command",
		Args:         map[string]any{"command": "git push"},
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		caps.mu.Lock()
		n := len(caps.calls)
		caps.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected a rationale patch")
}

// Fail-soft: a summarizer that returns empty clears the reserved slot instead of
// leaving a placeholder hanging.
func TestApprovalRationale_clearsPendingOnEmpty(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")
	caps := &patchCapture{}
	sum := &recordingSummarizer{out: "   "}
	attacher := toolhost.NewApprovalRationaleAttacher(toolhost.ApprovalRationaleDeps{
		Messages: memMessages{msgs: []api.Message{
			{Role: api.MessageRoleUser, Content: "git push"},
			{Role: api.MessageRoleAssistant, Content: "Pushing.", ToolCalls: []api.ToolCall{{ID: "tc1", Name: "command"}}},
		}},
		Workers:     memWorkers{},
		Progress:    memProgress{},
		Root:        rootID{},
		Summarizer:  sum,
		Checkpoints: caps,
	})
	attacher.AttachAsync(context.Background(), toolapproval.AIRationaleAttachRequest{
		CheckpointID: "chk-1",
		ToolContext:  tools.ToolContext{SessionID: "s1", ToolCallID: "tc1"},
		Tool:         "command",
		Args:         map[string]any{"command": "git push"},
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		caps.mu.Lock()
		clears := caps.clears
		patches := len(caps.calls)
		caps.mu.Unlock()
		if patches > 0 {
			t.Fatal("empty summary must not patch text")
		}
		if clears > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected the reserved slot to be cleared on empty summary")
}

// The global toggle disables the whole feature: no summarize, no patch, no clear.
func TestApprovalRationale_disabledDoesNothing(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	caps := &patchCapture{}
	sum := &recordingSummarizer{}
	attacher := toolhost.NewApprovalRationaleAttacher(toolhost.ApprovalRationaleDeps{
		Messages:    memMessages{},
		Workers:     memWorkers{},
		Progress:    memProgress{},
		Root:        rootID{},
		Summarizer:  sum,
		Checkpoints: caps,
		EnabledFn:   func() bool { return false },
	})
	if attacher.Enabled() {
		t.Fatal("Enabled() must reflect the disabled toggle")
	}
	attacher.AttachAsync(context.Background(), toolapproval.AIRationaleAttachRequest{
		CheckpointID: "chk-1",
		ToolContext:  tools.ToolContext{SessionID: "s1", ToolCallID: "tc1"},
		Tool:         "command",
	})
	caps.mu.Lock()
	defer caps.mu.Unlock()
	if caps.events != 0 {
		t.Fatalf("disabled attacher must not touch the checkpoint, got %d events", caps.events)
	}
	if sum.wasCalled() {
		t.Fatal("disabled attacher must not call the summarizer")
	}
}

func TestApprovalRationale_mockStub(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	caps := &patchCapture{}
	sum := &recordingSummarizer{}
	attacher := toolhost.NewApprovalRationaleAttacher(toolhost.ApprovalRationaleDeps{
		Messages: memMessages{msgs: []api.Message{
			{Role: api.MessageRoleUser, Content: "Ship the auth fix"},
			{Role: api.MessageRoleAssistant, Content: "Working."},
			{Role: api.MessageRoleTool, Content: "ok"},
			{Role: api.MessageRoleAssistant, Content: "Pushing.", ToolCalls: []api.ToolCall{{ID: "tc1", Name: "command"}}},
		}},
		Workers:     memWorkers{},
		Progress:    memProgress{content: "- [ ] Push the branch\n"},
		Root:        rootID{},
		Summarizer:  sum,
		Checkpoints: caps,
	})
	attacher.AttachAsync(context.Background(), toolapproval.AIRationaleAttachRequest{
		CheckpointID: "chk-2",
		ToolContext:  tools.ToolContext{SessionID: "s1", ToolCallID: "tc1"},
		Tool:         "command",
		Args:         map[string]any{"command": "git push"},
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		caps.mu.Lock()
		n := len(caps.calls)
		text := ""
		if n > 0 {
			text = caps.calls[0]
		}
		caps.mu.Unlock()
		if n > 0 {
			if text == "" {
				t.Fatal("empty stub")
			}
			if sum.wasCalled() {
				t.Fatal("mock path must not call summarizer")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for mock ai_rationale patch")
}

func (p *patchCapture) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
