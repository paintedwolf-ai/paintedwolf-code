package promptsource

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
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
