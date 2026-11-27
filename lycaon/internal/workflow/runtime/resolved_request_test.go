package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/internal/workflow/runtime"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// requestRuns serves one active child run with a resolved request and its parent.
type requestRuns struct {
	runstate.RunsRepository
	active    *wire.WorkflowRun
	parent    *wire.WorkflowRun
	activeErr error
	parentErr error
}

func (r requestRuns) ActiveStateBySession(context.Context, string) (*wire.WorkflowRun, map[string]any, error) {
	if r.activeErr != nil {
		return nil, nil, r.activeErr
	}
	vars := map[string]any{runstate.WorkflowRequestFeedbackID: map[string]any{
		"cadence": "once", "status": runstate.RequestStatusResolved, "text": "Plan the change",
	}}
	return r.active, vars, nil
}

func (r requestRuns) Get(context.Context, string) (*wire.WorkflowRun, error) {
	return r.parent, r.parentErr
}

// A store failure while finding where a request opened is reported, never read as "no request".
func TestResolvedRequestReportsUnreadableRuns(t *testing.T) {
	parentID := "parent-run"
	child := &wire.WorkflowRun{ID: "child-run", ParentRunID: &parentID}
	parent := &wire.WorkflowRun{ID: parentID, StartMessageID: "parent-start"}
	locked := errors.New("database is locked")
	for _, tc := range []struct {
		name        string
		runs        requestRuns
		wantErr     error
		wantOpening string
	}{
		{name: "inherited opening", runs: requestRuns{active: child, parent: parent}, wantOpening: "parent-start"},
		{name: "unreadable parent", runs: requestRuns{active: child, parentErr: locked}, wantErr: locked},
		{name: "missing parent", runs: requestRuns{active: child, parentErr: runstate.ErrNotFound}, wantErr: runstate.ErrNotFound},
		{name: "unreadable active run", runs: requestRuns{activeErr: locked}, wantErr: locked},
		{name: "no active run", runs: requestRuns{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := &runtime.SessionPolicy{Runs: tc.runs}
			got, err := policy.ResolvedRequest(t.Context(), "sess-1")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if got.OpeningMessageID != tc.wantOpening {
				t.Fatalf("opening = %q, want %q", got.OpeningMessageID, tc.wantOpening)
			}
		})
	}
}
