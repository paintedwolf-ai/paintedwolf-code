package promptsource

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/session/workerresults"
)

type Control struct {
	Batch          *batchcontrol.Service
	GracefulCancel *workerresults.GracefulCancel
	Holds          WorkflowHolds
	Processes      *processcontrol.Service
	Runtime        *coordinator.Runtime
	Workflow       WorkflowRunnable
}

func (m *Control) Build() promptloop.ControlDeps {
	deps := promptloop.ControlDeps{
		ReconcileCoordinatorBatch: func(ctx context.Context, sessionID string) {
			m.Batch.Reconcile(ctx, sessionID)
		},
	}
	deps.AssertRunnable = m.AssertRunnable
	deps.WorkerGracefulCancelPending = m.GracefulCancel.Pending
	deps.HumanApprovalAwaiting = func(ctx context.Context, sessionID string) bool {
		if m == nil || m.Holds == nil {
			return false
		}
		awaiting, err := m.Holds.HumanApprovalAwaiting(ctx, sessionID)
		return err == nil && awaiting
	}
	deps.HostObligationHeld = func(ctx context.Context, sessionID string) bool {
		if m == nil || m.Holds == nil {
			return false
		}
		held, err := m.Holds.HostObligationHeld(ctx, sessionID)
		return err == nil && held
	}
	deps.ParkBlockedLiveCommands = m.ParkBlockedLiveCommands

	return deps
}

func (m *Control) AssertRunnable(ctx context.Context, sessionID string) error {
	if m == nil || m.Workflow == nil {
		return nil
	}
	return m.Workflow.AssertSessionRunnable(ctx, sessionID)
}

func (m *Control) ParkBlockedLiveCommands(ctx context.Context, sessionID string) bool {
	if m == nil || m.Processes.Background == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	jobs := m.Processes.Background.ActiveJobs(sessionID)
	if len(jobs) == 0 {
		return false
	}
	handles := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if handle := strings.TrimSpace(job.Handle); handle != "" {
			handles = append(handles, handle)
		}
	}
	if len(handles) == 0 {
		return false
	}
	loop := m.Runtime.CoordinatorLoop()
	loop.EnterSleep(
		ctx,
		sessionID,
		time.Now().UTC().Add(time.Duration(loopwake.DefaultWaitSeconds)*time.Second),
		"waiting for active command after blocked turn",
		[]loopwake.WaitTrigger{loopwake.WaitTriggerTimer, loopwake.WaitTriggerProcessDone},
		handles,
		loopwake.SleepMoverHost,
	)
	loop.MarkWaitCalled(sessionID)
	// Completion between the job snapshot and EnterSleep needs an explicit wake.
	if !m.Processes.Background.HasRunningHandles(sessionID, handles) {
		loop.NudgeProcessFinished(ctx, sessionID, handles[0], anchor.Envelope{})
	}
	return true
}
