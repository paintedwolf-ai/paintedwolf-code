package postturn

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
)

type postTurnCalls struct {
	worker   bool
	mode     api.SessionMode
	emits    int
	audit    error
	takeErr  error
	takes    int
	audited  bool
	takeLast bool
}

func (c *postTurnCalls) Get(context.Context, string) (*api.Session, error) {
	sess := &api.Session{ID: "coordinator", Mode: c.mode}
	if c.worker {
		sess.ParentSessionID = "parent"
	}
	return sess, nil
}
func (c *postTurnCalls) GetMessages(context.Context, string) ([]api.Message, error) { return nil, nil }
func (c *postTurnCalls) AfterPrompt(context.Context, string, []string) error {
	c.audited = true
	return c.audit
}
func (c *postTurnCalls) Emit(context.Context, string, anchor.ID, anchor.Envelope)          { c.emits++ }
func (c *postTurnCalls) QueueAdvisories(context.Context, string, []guidance.GuidanceNudge) {}
func (c *postTurnCalls) TakePolicy(context.Context, string) ([]api.Message, error) {
	c.takes++
	c.takeLast = c.audited
	return nil, c.takeErr
}

// Whatever way the hooks end, a coordinator's post-turn feedback commits before the
// session can take another user message, so it never lands under that question.
func TestEveryPostTurnExitCommitsFeedbackWithTheEndedTurn(t *testing.T) {
	escalated := guidance.ErrGroundingEscalated
	for name, tc := range map[string]struct {
		calls    *postTurnCalls
		grounded bool
		wantErr  error
	}{
		"audit passes":    {calls: &postTurnCalls{}, grounded: true},
		"audit escalates": {calls: &postTurnCalls{audit: escalated}, grounded: true, wantErr: escalated},
		"audit nudges":    {calls: &postTurnCalls{audit: &guidance.ErrGroundingNudge{}}, grounded: true},
		"audit errors":    {calls: &postTurnCalls{audit: errors.New("audit failed")}, grounded: true},
		"no audit":        {calls: &postTurnCalls{}},
		"commit fails":    {calls: &postTurnCalls{takeErr: errors.New("store unavailable")}, grounded: true},
	} {
		t.Run(name, func(t *testing.T) {
			m := New(tc.calls, tc.calls, tc.calls)
			if tc.grounded {
				m.SetGrounding(tc.calls)
			}
			err := m.After(t.Context(), "coordinator", "coordinator", nil)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.calls.takes != 1 {
				t.Fatalf("feedback commits = %d, want 1", tc.calls.takes)
			}
			if tc.grounded && !tc.calls.takeLast {
				t.Fatal("feedback committed before the audit that produces it")
			}
		})
	}
}

// A worker's turn has no next user message, and its feedback stays for its own loop.
func TestWorkerPostTurnLeavesFeedbackStaged(t *testing.T) {
	calls := &postTurnCalls{worker: true}
	testutil.FailErr(t, "after", New(calls, calls, calls).After(t.Context(), "worker", "implementer", nil))
	if calls.takes != 0 {
		t.Fatalf("feedback commits = %d, want 0", calls.takes)
	}
}

func TestProgressMissingSkipsAskModeAndUnofferedTools(t *testing.T) {
	askCalls := &postTurnCalls{mode: api.SessionModeAsk}
	askSvc := New(askCalls, askCalls, askCalls)
	askSvc.SetProgress(progresstest.New(t, "coordinator"))
	askSvc.ProgressMissing(t.Context(), "coordinator", []string{"write"})
	if askCalls.emits != 0 {
		t.Fatalf("Ask mode emitted %d progress-missing nudges, want 0", askCalls.emits)
	}

	agentCalls := &postTurnCalls{mode: api.SessionModeAgent}
	agentSvc := New(agentCalls, agentCalls, agentCalls)
	agentSvc.SetProgress(progresstest.New(t, "coordinator"))
	ctx := toolreach.WithRequest(t.Context(), toolreach.Offered([]string{"summarize", "update_progress"}))
	agentSvc.ProgressMissing(ctx, "coordinator", []string{"write"})
	if agentCalls.emits != 0 {
		t.Fatalf("unoffered tool emitted %d progress-missing nudges, want 0", agentCalls.emits)
	}

	ctxOffered := toolreach.WithRequest(t.Context(), toolreach.Offered([]string{"task", "update_progress"}))
	agentSvc.ProgressMissing(ctxOffered, "coordinator", []string{"task"})
	if agentCalls.emits != 1 {
		t.Fatalf("offered gated tool emitted %d progress-missing nudges, want 1", agentCalls.emits)
	}
}
