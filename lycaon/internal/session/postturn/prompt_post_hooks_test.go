package postturn

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type postTurnCalls struct {
	worker   bool
	audit    error
	takeErr  error
	takes    int
	audited  bool
	takeLast bool
}

func (c *postTurnCalls) Get(context.Context, string) (*api.Session, error) {
	sess := &api.Session{ID: "coordinator"}
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
func (c *postTurnCalls) Emit(context.Context, string, anchor.ID, anchor.Envelope)          {}
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
			m := New(tc.calls, tc.calls)
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
	testutil.FailErr(t, "after", New(calls, calls).After(t.Context(), "worker", "implementer", nil))
	if calls.takes != 0 {
		t.Fatalf("feedback commits = %d, want 0", calls.takes)
	}
}
