package coordinatorcontrol

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	setupanchor "github.com/lycaon/lycaon/internal/testsetup/anchor"
)

func TestWorkerRoundNoticeAndMessageNeedsReplyQueueCoordinatorKicks(t *testing.T) {
	setupanchor.Install()
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{})
	workers := &Workers{Runtime: rt}

	var nilWorkers *Workers
	nilWorkers.RoundNotice(t.Context(), "parent-nil", kick.WorkerRoundNoticeFacts{JobID: "job-1"})
	nilWorkers.MessageNeedsReply(t.Context(), "parent-nil", "job-1")
	workers.RoundNotice(t.Context(), "", kick.WorkerRoundNoticeFacts{JobID: "job-1"})
	workers.RoundNotice(t.Context(), "parent-empty-job", kick.WorkerRoundNoticeFacts{})
	workers.MessageNeedsReply(t.Context(), "", "job-1")
	workers.MessageNeedsReply(t.Context(), "parent-empty-job", "   ")
	if _, ok := rt.Anchors().Kicks().PeekPendingKickID("parent-empty-job"); ok {
		t.Fatal("empty job id queued a coordinator kick")
	}

	workers.RoundNotice(t.Context(), "parent-notice", kick.WorkerRoundNoticeFacts{
		JobID:      "job-1",
		AgentType:  "implementer",
		Kind:       kick.RoundNoticeConsumption,
		ModelCalls: 25,
	})
	if _, ok := rt.Anchors().Kicks().PeekPendingKickID("parent-notice"); !ok {
		t.Fatal("round notice did not queue a coordinator kick")
	}

	workers.MessageNeedsReply(t.Context(), "parent-reply", "job-2")
	if _, ok := rt.Anchors().Kicks().PeekPendingKickID("parent-reply"); !ok {
		t.Fatal("message needs reply did not queue a coordinator kick")
	}
}
