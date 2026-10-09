package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

type proofCountSessions struct {
	proofCalls    atomic.Int32
	nudgeCalls    atomic.Int32
	terminalCalls atomic.Int32
	armedJobs     []string
	shouldNudge   bool
	proofErr      error
}

func (s *proofCountSessions) RecordTerminalProof(context.Context, string, string, string) error {
	s.proofCalls.Add(1)
	return s.proofErr
}

type outcomeCountRecorder struct {
	completeCalls atomic.Int32
}

func (r *outcomeCountRecorder) OnWorkerComplete(context.Context, string, api.WorkerResult) error {
	r.completeCalls.Add(1)
	return nil
}

func (*outcomeCountRecorder) OnWorkerFailed(context.Context, string, error) error { return nil }

type noTaskSessions struct{ *proofCountSessions }

func (*noTaskSessions) TaskByID(string) (workeroutcomes.SummaryInput, bool) {
	return workeroutcomes.SummaryInput{}, false
}

func (s *proofCountSessions) TaskByID(string) (workeroutcomes.SummaryInput, bool) {
	return workeroutcomes.SummaryInput{
		ParentSessionID: "parent-1",
		LegID:           "",
		DelegationID:    "",
	}, true
}

func (*proofCountSessions) ProjectResult(_ context.Context, _ workeroutcomes.SummaryInput, result api.WorkerResult) (string, error) {
	return result.Status, nil
}

func (*proofCountSessions) ProjectFailure(context.Context, workeroutcomes.SummaryInput, error) error {
	return nil
}

func (s *proofCountSessions) NotifyWorkerCycleTerminal(context.Context, string, string) {
	s.terminalCalls.Add(1)
}

func (s *proofCountSessions) NudgeLegFinishedLoopWake(context.Context, string, time.Time, string) {}

func (s *proofCountSessions) ShouldNudge(context.Context, string, string, string) bool {
	return s.shouldNudge
}

func (s *proofCountSessions) NudgeCoordinatorLoopAfterWorkerJobTerminal(context.Context, string, string, anchor.Envelope) {
	s.nudgeCalls.Add(1)
}

func (s *proofCountSessions) EnvelopeForTerminal(context.Context, string, string) anchor.Envelope {
	return anchor.Envelope{}
}

func (s *proofCountSessions) Arm(_ context.Context, _ string, jobID string) {
	s.armedJobs = append(s.armedJobs, jobID)
}

// Only work that completed latches the parent checklist; a partial, failed,
// or cancelled leg has closed nothing and its resume must not be blocked.
func TestOnlyCompletedWorkArmsTheProgressLatch(t *testing.T) {
	for _, tc := range []struct {
		status string
		armed  bool
	}{
		{"complete", true},
		{"open", true},
		{"partial", false},
		{"needs_decision", false},
		{"cancelled", false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			sessions := &proofCountSessions{shouldNudge: true}
			bridge := &SessionOutcomeBridge{Sessions: sessions, Results: sessions, State: sessions, Closure: sessions}
			if err := bridge.OnWorkerComplete(t.Context(), "job-1", api.WorkerResult{Status: tc.status}); err != nil {
				t.Fatalf("OnWorkerComplete: %v", err)
			}
			if armed := len(sessions.armedJobs) == 1 && sessions.armedJobs[0] == "job-1"; armed != tc.armed {
				t.Fatalf("%s armed jobs = %v want armed=%v", tc.status, sessions.armedJobs, tc.armed)
			}
		})
	}
	sessions := &proofCountSessions{shouldNudge: true}
	bridge := &SessionOutcomeBridge{Sessions: sessions, Results: sessions, State: sessions, Closure: sessions}
	if err := bridge.OnWorkerFailed(t.Context(), "job-1", errors.New("provider failed")); err != nil {
		t.Fatalf("OnWorkerFailed: %v", err)
	}
	if len(sessions.armedJobs) != 0 {
		t.Fatalf("failed worker armed %v", sessions.armedJobs)
	}
}

func TestOnWorkerCompleteRecordsTerminalProofOnce(t *testing.T) {
	sessions := &proofCountSessions{shouldNudge: true}
	bridge := &SessionOutcomeBridge{Sessions: sessions, Results: sessions, State: sessions, Closure: sessions}
	ctx := context.Background()
	result := api.WorkerResult{Status: "complete"}
	if err := bridge.OnWorkerComplete(ctx, "job-1", result); err != nil {
		t.Fatalf("OnWorkerComplete: %v", err)
	}
	if got := sessions.proofCalls.Load(); got != 1 {
		t.Fatalf("RecordWorkerTerminalProofAfterQueueComplete calls = %d want 1", got)
	}
	if got := sessions.nudgeCalls.Load(); got != 1 {
		t.Fatalf("coordinator nudge calls = %d want 1", got)
	}
}

func TestOnWorkerCompleteDefersWakeWhenProofFails(t *testing.T) {
	proofErr := errors.New("workflow run revision conflict")
	sessions := &proofCountSessions{shouldNudge: true, proofErr: proofErr}
	inner := &outcomeCountRecorder{}
	bridge := &SessionOutcomeBridge{Sessions: sessions, Results: sessions, State: sessions, Closure: sessions, Inner: inner}
	err := bridge.OnWorkerComplete(t.Context(), "job-1", api.WorkerResult{Status: "complete"})
	if err == nil {
		t.Fatal("terminal proof failure must still surface as an error")
	}
	if !errors.Is(err, proofErr) {
		t.Fatalf("error = %v want it to wrap %v", err, proofErr)
	}
	if got := inner.completeCalls.Load(); got != 0 {
		t.Fatalf("inner completion calls = %d want 0 before proof settles", got)
	}
	if got := sessions.nudgeCalls.Load(); got != 0 {
		t.Fatalf("coordinator nudge calls = %d want 0 until projection retries", got)
	}
}

func TestOnWorkerCompleteStillSettlesInnerRecorderWithoutSessionTask(t *testing.T) {
	inner := &outcomeCountRecorder{}
	bridge := &SessionOutcomeBridge{
		Sessions: &noTaskSessions{proofCountSessions: &proofCountSessions{}},
		Results:  &noTaskSessions{proofCountSessions: &proofCountSessions{}},
		Inner:    inner,
	}
	if err := bridge.OnWorkerComplete(t.Context(), "job-1", api.WorkerResult{Status: "complete"}); err != nil {
		t.Fatalf("OnWorkerComplete: %v", err)
	}
	if got := inner.completeCalls.Load(); got != 1 {
		t.Fatalf("inner completion calls = %d want 1", got)
	}
}

func TestOnWorkerCompletePartialWakesCoordinator(t *testing.T) {
	sessions := &proofCountSessions{shouldNudge: true}
	bridge := &SessionOutcomeBridge{Sessions: sessions, Results: sessions, State: sessions, Closure: sessions}
	ctx := context.Background()
	result := api.WorkerResult{Status: "partial"}
	if err := bridge.OnWorkerComplete(ctx, "job-partial", result); err != nil {
		t.Fatalf("OnWorkerComplete: %v", err)
	}
	if got := sessions.nudgeCalls.Load(); got != 1 {
		t.Fatalf("coordinator nudge calls = %d want 1 for partial worker", got)
	}
}

func TestOnWorkerCompleteCanceledSkipsCoordinatorWake(t *testing.T) {
	sessions := &proofCountSessions{shouldNudge: true}
	bridge := &SessionOutcomeBridge{Sessions: sessions, Results: sessions, State: sessions, Closure: sessions}
	ctx := context.Background()
	result := api.WorkerResult{Status: "canceled"}
	if err := bridge.OnWorkerComplete(ctx, "job-canceled", result); err != nil {
		t.Fatalf("OnWorkerComplete: %v", err)
	}
	if got := sessions.nudgeCalls.Load(); got != 0 {
		t.Fatalf("coordinator nudge calls = %d want 0 for canceled worker", got)
	}
}

func TestOnWorkerCompleteSkipsNudgeWhenShouldNudgeFalse(t *testing.T) {
	sessions := &proofCountSessions{shouldNudge: false}
	bridge := &SessionOutcomeBridge{Sessions: sessions, Results: sessions, State: sessions, Closure: sessions}
	ctx := context.Background()
	result := api.WorkerResult{Status: "complete"}
	if err := bridge.OnWorkerComplete(ctx, "job-1", result); err != nil {
		t.Fatalf("OnWorkerComplete: %v", err)
	}
	if got := sessions.proofCalls.Load(); got != 1 {
		t.Fatalf("proof calls = %d want 1", got)
	}
	if got := sessions.nudgeCalls.Load(); got != 0 {
		t.Fatalf("coordinator nudge calls = %d want 0 when ShouldNudge false", got)
	}
}

func TestOnWorkerFailedWakesCoordinator(t *testing.T) {
	sessions := &proofCountSessions{shouldNudge: true}
	bridge := &SessionOutcomeBridge{Sessions: sessions, Results: sessions, State: sessions, Closure: sessions}
	ctx := context.Background()
	if err := bridge.OnWorkerFailed(ctx, "job-dead", errors.New("provider openai-1: openai error 400")); err != nil {
		t.Fatalf("OnWorkerFailed: %v", err)
	}
	if got := sessions.nudgeCalls.Load(); got != 1 {
		t.Fatalf("coordinator nudge calls = %d want 1 for failed worker", got)
	}
}

func TestOnWorkerFailedCancelSkipsCoordinatorWake(t *testing.T) {
	sessions := &proofCountSessions{shouldNudge: true}
	bridge := &SessionOutcomeBridge{Sessions: sessions, Results: sessions, State: sessions, Closure: sessions}
	ctx := context.Background()
	if err := bridge.OnWorkerFailed(ctx, "job-cancel", context.Canceled); err != nil {
		t.Fatalf("OnWorkerFailed: %v", err)
	}
	if got := sessions.nudgeCalls.Load(); got != 0 {
		t.Fatalf("coordinator nudge calls = %d want 0 for canceled worker", got)
	}
}

func TestOnWorkerFailedSkipsNudgeWhenShouldNudgeFalse(t *testing.T) {
	sessions := &proofCountSessions{shouldNudge: false}
	bridge := &SessionOutcomeBridge{Sessions: sessions, Results: sessions, State: sessions, Closure: sessions}
	ctx := context.Background()
	if err := bridge.OnWorkerFailed(ctx, "job-dead", errors.New("host death")); err != nil {
		t.Fatalf("OnWorkerFailed: %v", err)
	}
	if got := sessions.nudgeCalls.Load(); got != 0 {
		t.Fatalf("coordinator nudge calls = %d want 0 when ShouldNudge false", got)
	}
}

func TestOnWorkerCompleteRecordsProofForDelegationLeg(t *testing.T) {
	sessions := &proofCountSessions{shouldNudge: true}
	leg := &legSessions{
		proofCountSessions: sessions,
		legID:              "leg-1",
	}
	bridge := &SessionOutcomeBridge{Sessions: leg, Results: leg, State: leg, Closure: leg}
	ctx := context.Background()
	result := api.WorkerResult{Status: "complete"}
	if err := bridge.OnWorkerComplete(ctx, "job-2", result); err != nil {
		t.Fatalf("OnWorkerComplete: %v", err)
	}
	if got := sessions.proofCalls.Load(); got != 1 {
		t.Fatalf("proof calls = %d want 1 for delegation leg", got)
	}
}

type legSessions struct {
	*proofCountSessions
	legID string
}

func (s *legSessions) TaskByID(string) (workeroutcomes.SummaryInput, bool) {
	return workeroutcomes.SummaryInput{
		ParentSessionID: "parent-1",
		LegID:           s.legID,
		DelegationID:    "dep-1",
	}, true
}
