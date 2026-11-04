package postturn

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type postTurnCalls struct {
	order   []string
	takeErr error
}

func (c *postTurnCalls) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{ID: "coordinator"}, nil
}
func (c *postTurnCalls) GetMessages(context.Context, string) ([]api.Message, error) { return nil, nil }
func (c *postTurnCalls) AfterPrompt(context.Context, string, []string) error {
	c.order = append(c.order, "grounding")
	return nil
}
func (c *postTurnCalls) Emit(context.Context, string, anchor.ID, anchor.Envelope)          {}
func (c *postTurnCalls) QueueAdvisories(context.Context, string, []guidance.GuidanceNudge) {}
func (c *postTurnCalls) TakePolicy(context.Context, string) ([]api.Message, error) {
	c.order = append(c.order, "take")
	return nil, c.takeErr
}

// Feedback on the ended turn is committed before the session can take another
// user message, so it never lands after that message's question.
func TestAfterCommitsPostTurnFeedbackWithTheEndedTurn(t *testing.T) {
	calls := &postTurnCalls{}
	m := New(calls, calls)
	m.SetGrounding(calls)
	testutil.FailErr(t, "after", m.After(t.Context(), "coordinator", "coordinator", nil))
	if got := strings.Join(calls.order, ","); got != "grounding,take" {
		t.Fatalf("order = %s, want the audit before its commit", got)
	}
}

// A failed commit leaves the feedback staged for the next turn and ends this one cleanly.
func TestAfterToleratesAFailedFeedbackCommit(t *testing.T) {
	calls := &postTurnCalls{takeErr: errors.New("store unavailable")}
	m := New(calls, calls)
	m.SetGrounding(calls)
	testutil.FailErr(t, "after", m.After(t.Context(), "coordinator", "coordinator", nil))
}
