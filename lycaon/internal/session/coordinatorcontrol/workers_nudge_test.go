package coordinatorcontrol

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/settings"
	setupanchor "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerRoundNoticeAndMessageNeedsReplyWakeWaitingCoordinator(t *testing.T) {
	setupanchor.Install()
	informs := map[string]anchor.ID{}
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{
		LoopWakeDeps: func() loopwake.LoopDeps {
			return loopwake.LoopDeps{
				GetSession: func(_ context.Context, sessionID string) (*api.Session, error) {
					return &api.Session{ID: sessionID, Status: api.SessionStatusIdle}, nil
				},
				IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
				Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
				WorkerCycleIdle:      func(context.Context, *api.Session, string) (bool, error) { return true, nil },
				QueueInform: func(_ context.Context, sessionID string, inform anchor.ID, _ anchor.Envelope) {
					informs[sessionID] = inform
				},
			}
		},
	})
	workers := &Workers{Runtime: rt}
	loop := rt.CoordinatorLoop()
	deadline := time.Now().UTC().Add(10 * time.Minute)
	triggers := []loopwake.WaitTrigger{loopwake.WaitTriggerTimer, loopwake.WaitTriggerAllWorkersIdle}

	var nilWorkers *Workers
	nilWorkers.RoundNotice(t.Context(), "parent-nil", kick.WorkerRoundNoticeFacts{JobID: "job-1"})
	nilWorkers.MessageNeedsReply(t.Context(), "parent-nil", "job-1")

	loop.Waits.EnterSleep(t.Context(), "parent-empty-job", deadline, "waiting", triggers, nil, loopwake.SleepMoverHost)
	workers.RoundNotice(t.Context(), "", kick.WorkerRoundNoticeFacts{JobID: "job-1"})
	workers.RoundNotice(t.Context(), "parent-empty-job", kick.WorkerRoundNoticeFacts{})
	workers.MessageNeedsReply(t.Context(), "", "job-1")
	workers.MessageNeedsReply(t.Context(), "parent-empty-job", "   ")
	if !loop.Waits.IsSleeping("parent-empty-job") {
		t.Fatal("empty job id woke a sleeping coordinator")
	}

	loop.Waits.EnterSleep(t.Context(), "parent-notice", deadline, "waiting", triggers, nil, loopwake.SleepMoverHost)
	workers.RoundNotice(t.Context(), "parent-notice", kick.WorkerRoundNoticeFacts{
		JobID:      "job-1",
		AgentType:  "implementer",
		Kind:       kick.RoundNoticeConsumption,
		ModelCalls: 25,
	})
	if loop.Waits.IsSleeping("parent-notice") || informs["parent-notice"] != anchor.WorkerRoundNotice {
		t.Fatalf("round notice did not wake coordinator: sleeping=%v inform=%q", loop.Waits.IsSleeping("parent-notice"), informs["parent-notice"])
	}

	loop.Waits.EnterSleep(t.Context(), "parent-reply", deadline, "waiting", triggers, nil, loopwake.SleepMoverHost)
	workers.MessageNeedsReply(t.Context(), "parent-reply", "job-2")
	if loop.Waits.IsSleeping("parent-reply") {
		t.Fatal("message needs reply did not wake coordinator")
	}
}
