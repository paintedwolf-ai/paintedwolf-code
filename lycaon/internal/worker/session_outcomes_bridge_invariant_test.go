package worker

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/progressclosure"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

// Compile-time invariant assertions ensuring session domain implementations
// fulfill the narrow peer contracts declared by the worker package.
var (
	_ WorkerResultProjection = (*workeroutcomes.Results)(nil)
	_ WorkerCycleWakeState   = (*workeroutcomes.State)(nil)
	_ ProgressClosureArm     = (*progressclosure.Service)(nil)
)

type invariantMockSessions struct {
	task               workeroutcomes.SummaryInput
	taskFound          bool
	projectResultErr   error
	projectFailureErr  error
	terminalProofErr   error
	proofCalled        bool
	nudgeCalled        bool
	armCalled          bool
	terminalParentID   string
	terminalJobID      string
	terminalCallsCount atomic.Int32
}

func (m *invariantMockSessions) TaskByID(string) (workeroutcomes.SummaryInput, bool) {
	return m.task, m.taskFound
}

func (m *invariantMockSessions) ProjectResult(context.Context, workeroutcomes.SummaryInput, api.WorkerResult) (string, error) {
	if m.projectResultErr != nil {
		return "", m.projectResultErr
	}
	return "complete", nil
}

func (m *invariantMockSessions) ProjectFailure(context.Context, workeroutcomes.SummaryInput, error) error {
	return m.projectFailureErr
}

func (m *invariantMockSessions) RecordTerminalProof(context.Context, string, string, string) error {
	m.proofCalled = true
	return m.terminalProofErr
}

func (m *invariantMockSessions) EnvelopeForTerminal(context.Context, string, string) anchor.Envelope {
	return anchor.Envelope{}
}

func (m *invariantMockSessions) ShouldNudge(context.Context, string, string, string) bool {
	return true
}

func (m *invariantMockSessions) Arm(context.Context, string, string) {
	m.armCalled = true
}

func (m *invariantMockSessions) Terminal(_ context.Context, parentID, jobID string) {
	m.terminalParentID = parentID
	m.terminalJobID = jobID
	m.terminalCallsCount.Add(1)
}

func (m *invariantMockSessions) AfterTerminal(context.Context, string, string, anchor.Envelope) {
	m.nudgeCalled = true
}

func (m *invariantMockSessions) NudgeLegFinished(context.Context, string, interface{}, string) {}

func TestSessionOutcomeBridge_InvariantProjectResultError(t *testing.T) {
	ctx := context.Background()
	expectedErr := errors.New("boom project result")
	mock := &invariantMockSessions{
		taskFound: true,
		task: workeroutcomes.SummaryInput{
			ParentSessionID: "parent-1",
			JobID:           "job-1",
		},
		projectResultErr: expectedErr,
	}

	bridge := &SessionOutcomeBridge{
		Results: mock,
		State:   mock,
		Workers: mock,
		Closure: mock,
	}

	err := bridge.OnWorkerComplete(ctx, "job-1", api.WorkerResult{Status: "complete"})
	if err == nil {
		t.Fatal("expected error from OnWorkerComplete, got nil")
	}
	if !strings.Contains(err.Error(), "project worker result") || !errors.Is(err, expectedErr) {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.proofCalled {
		t.Fatal("RecordTerminalProof must not be called when ProjectResult fails")
	}
	if mock.nudgeCalled {
		t.Fatal("nudge must not be triggered when ProjectResult fails")
	}
	if mock.armCalled {
		t.Fatal("closure must not be armed when ProjectResult fails")
	}
}

func TestSessionOutcomeBridge_InvariantRecordTerminalProofError(t *testing.T) {
	ctx := context.Background()
	expectedErr := errors.New("proof recording conflict")
	mock := &invariantMockSessions{
		taskFound: true,
		task: workeroutcomes.SummaryInput{
			ParentSessionID: "parent-1",
			JobID:           "job-1",
		},
		terminalProofErr: expectedErr,
	}

	bridge := &SessionOutcomeBridge{
		Results: mock,
		State:   mock,
		Workers: mock,
		Closure: mock,
	}

	err := bridge.OnWorkerComplete(ctx, "job-1", api.WorkerResult{Status: "complete"})
	if err == nil {
		t.Fatal("expected error from OnWorkerComplete, got nil")
	}
	if !strings.Contains(err.Error(), "record worker terminal proof") || !errors.Is(err, expectedErr) {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mock.proofCalled {
		t.Fatal("RecordTerminalProof was expected to be called")
	}
	if mock.nudgeCalled {
		t.Fatal("nudge must not be triggered when RecordTerminalProof fails")
	}
	if mock.armCalled {
		t.Fatal("closure must not be armed when RecordTerminalProof fails")
	}
}

func TestSessionOutcomeBridge_InvariantProjectFailureError(t *testing.T) {
	ctx := context.Background()
	expectedErr := errors.New("boom project failure")
	mock := &invariantMockSessions{
		taskFound: true,
		task: workeroutcomes.SummaryInput{
			ParentSessionID: "parent-1",
			JobID:           "job-1",
		},
		projectFailureErr: expectedErr,
	}

	bridge := &SessionOutcomeBridge{
		Results: mock,
		State:   mock,
		Workers: mock,
		Closure: mock,
	}

	err := bridge.OnWorkerFailed(ctx, "job-1", errors.New("worker crash"))
	if err == nil {
		t.Fatal("expected error from OnWorkerFailed, got nil")
	}
	if !strings.Contains(err.Error(), "project worker failure") || !errors.Is(err, expectedErr) {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.nudgeCalled {
		t.Fatal("nudge must not be triggered when ProjectFailure fails")
	}
}

func TestSessionOutcomeBridge_InvariantOnOutcomeDelivered(t *testing.T) {
	ctx := context.Background()
	mock := &invariantMockSessions{}
	bridge := &SessionOutcomeBridge{
		Workers: mock,
	}

	// 1. With parent session ID, release terminal notification.
	task := api.WorkerTask{
		ID:              "task-123",
		ParentSessionID: "parent-456",
	}
	bridge.OnOutcomeDelivered(ctx, task)
	if mock.terminalCallsCount.Load() != 1 {
		t.Fatalf("expected 1 terminal call, got %d", mock.terminalCallsCount.Load())
	}
	if mock.terminalParentID != "parent-456" || mock.terminalJobID != "task-123" {
		t.Fatalf("unexpected terminal args: parent=%q job=%q", mock.terminalParentID, mock.terminalJobID)
	}

	// 2. Empty parent session ID should not notify terminal.
	mock.terminalCallsCount.Store(0)
	taskNoParent := api.WorkerTask{
		ID:              "task-123",
		ParentSessionID: "   ",
	}
	bridge.OnOutcomeDelivered(ctx, taskNoParent)
	if mock.terminalCallsCount.Load() != 0 {
		t.Fatalf("expected 0 terminal calls for empty parent, got %d", mock.terminalCallsCount.Load())
	}

	// 3. Nil Workers should not panic.
	nilWorkersBridge := &SessionOutcomeBridge{}
	nilWorkersBridge.OnOutcomeDelivered(ctx, task)
}
