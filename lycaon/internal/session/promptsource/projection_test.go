package promptsource

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/session/history"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type projectionStore struct {
	*store.Memory
	appendErr error
}

func (s projectionStore) AppendMessages(ctx context.Context, sessionID string, messages ...api.Message) error {
	if s.appendErr != nil {
		return s.appendErr
	}
	return s.Memory.AppendMessages(ctx, sessionID, messages...)
}

type reviewAccounting func(context.Context, string, api.Message) error

func (f reviewAccounting) RecordReviewToolResult(ctx context.Context, sessionID string, message api.Message) error {
	return f(ctx, sessionID, message)
}

// Accounting consumes the committed batch and fails the turn when its journal
// cannot settle. A transcript failure must never spend a repair attempt.
func TestProjectionAppendSettlesTranscriptBeforeReviewAccounting(t *testing.T) {
	storageFailure := errors.New("transcript unavailable")
	accountingFailure := errors.New("repair journal unavailable")
	for _, tc := range []struct {
		name                  string
		storageErr, reviewErr error
		withoutReviews        bool
		wantAccounted         []string
	}{
		{name: "committed batch", wantAccounted: []string{"first", "second"}},
		{name: "accounting failure", reviewErr: accountingFailure, wantAccounted: []string{"first"}},
		{name: "storage failure", storageErr: storageFailure},
		{name: "without review binding", withoutReviews: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memory := store.NewMemory()
			session, err := memory.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
			testutil.FailErr(t, "create projection session", err)
			source := projectionStore{Memory: memory, appendErr: tc.storageErr}
			projection := &Projection{Sessions: memory, Transcript: transcript.New(source, nil)}
			var accounted []string
			if !tc.withoutReviews {
				projection.Reviews = reviewAccounting(func(ctx context.Context, id string, message api.Message) error {
					if id != session.ID {
						t.Fatalf("accounting session = %q", id)
					}
					durable, err := memory.GetMessages(ctx, id)
					testutil.FailErr(t, "read committed batch", err)
					if len(durable) != 2 || durable[0].ID != "first" || durable[1].ID != "second" {
						t.Fatalf("accounting saw incomplete transcript: %+v", durable)
					}
					accounted = append(accounted, message.ID)
					return tc.reviewErr
				})
			}
			deps := projection.Build()
			err = deps.AppendMessages(t.Context(), session.ID,
				api.Message{ID: "first", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "submit_verdict", ToolCallID: "call-1"}},
				api.Message{ID: "second", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "submit_verdict", ToolCallID: "call-2"}},
			)
			expectedErr := tc.reviewErr
			if tc.storageErr != nil {
				expectedErr = tc.storageErr
			}
			if !errors.Is(err, expectedErr) {
				t.Fatalf("append error = %v, want %v", err, expectedErr)
			}
			if !slices.Equal(accounted, tc.wantAccounted) {
				t.Fatalf("accounted = %v, want %v", accounted, tc.wantAccounted)
			}
			durable, err := memory.GetMessages(t.Context(), session.ID)
			testutil.FailErr(t, "read final transcript", err)
			expectedRows := 2
			if tc.storageErr != nil {
				expectedRows = 0
			}
			if len(durable) != expectedRows {
				t.Fatalf("durable rows = %d, want %d", len(durable), expectedRows)
			}
		})
	}
}

func TestModelBuildWiresContextOverflowAndTokenObservationCallbacks(t *testing.T) {
	memory := store.NewMemory()
	sess, err := memory.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append message", memory.AppendMessages(t.Context(), sess.ID, api.Message{
		ID: "m1", Role: api.MessageRoleUser, Content: "hello",
	}))
	lim := sessionlimits.New(settings.DefaultSessionLimits(), nil)
	hist := history.New(memory, &lifecycle.State{}, nil, lim, nil, nil)
	m := &Model{History: hist, Limits: lim}
	deps := m.Build()

	deps.RecordCompactionTokenObservation(sess.ID, "openai-compatible", "qwen", 4096, 3500)
	if rows, err := deps.CompactAfterContextOverflow(t.Context(), nil, []api.Message{{ID: "m0"}}, 16384); err != nil || len(rows) != 1 {
		t.Fatalf("nil session CompactAfterContextOverflow = (%v, %v)", rows, err)
	}
	rows, _ := deps.CompactAfterContextOverflow(t.Context(), sess, []api.Message{{ID: "m1", Role: api.MessageRoleUser, Content: "hello"}}, 16384)
	if len(rows) == 0 {
		t.Fatal("expected non-empty rows after CompactAfterContextOverflow")
	}
}

func TestProjectionBuildForwardsSettledModelOutputIteration(t *testing.T) {
	ctx := t.Context()
	memory := store.NewMemory()
	sess, err := memory.Create(ctx, api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)
	turn, err := memory.BeginTurn(ctx, store.TurnStart{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: store.TurnOriginUser, InputJSON: "{}",
	})
	testutil.FailErr(t, "begin turn", err)

	projection := &Projection{Sessions: memory, Transcript: transcript.New(memory, nil)}
	deps := projection.Build()
	iter, err := deps.SettledModelOutputIteration(ctx, turn.Attempt.ID)
	testutil.FailErr(t, "initial settled iteration", err)
	if iter != 0 {
		t.Fatalf("initial settled iteration = %d, want 0", iter)
	}
	_, err = deps.SettleModelOutput(ctx, store.ModelOutput{
		ID: "out-1", TurnAttemptID: turn.Attempt.ID, SessionID: sess.ID,
		Iteration: 2, MessageID: "msg-1", Content: "Done.",
	})
	testutil.FailErr(t, "settle model output", err)
	iter, err = deps.SettledModelOutputIteration(ctx, turn.Attempt.ID)
	testutil.FailErr(t, "settled iteration after output", err)
	if iter != 2 {
		t.Fatalf("settled iteration = %d, want 2", iter)
	}
}
