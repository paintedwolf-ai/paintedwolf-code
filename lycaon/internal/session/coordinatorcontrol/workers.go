package coordinatorcontrol

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	"github.com/lycaon/lycaon/internal/session/turnadmission"
	"github.com/lycaon/lycaon/internal/session/turnsettlement"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
)

type Workers struct {
	Runtime    *coordinator.Runtime
	Batch      *batchcontrol.Service
	Settlement *turnsettlement.Service
	Admission  *turnadmission.Service
	Digests    *workeroutcomes.Digests
	Results    *workeroutcomes.Results
}

func (m *Workers) Terminal(ctx context.Context, parentID, completingJobID string) {
	if m == nil {
		return
	}
	m.Runtime.CoordinatorLoop().Cycles.OnWorkerCycleTerminal(ctx, parentID, completingJobID)
	m.Batch.Reconcile(ctx, parentID)
	m.Batch.DisarmTerminal(ctx, parentID)
	m.Settlement.ReconcileSandbox(ctx, parentID)
	m.Admission.MaybePromote(ctx, parentID)
	m.Digests.Forget(completingJobID)
}

// RoundNotice tells a worker's coordinator about its running round.
func (m *Workers) RoundNotice(ctx context.Context, parentID string, facts kick.WorkerRoundNoticeFacts) {
	parentID = strings.TrimSpace(parentID)
	if m == nil || parentID == "" || strings.TrimSpace(facts.JobID) == "" {
		return
	}
	m.Runtime.CoordinatorLoop().Nudges.NudgeWorkerRoundNotice(ctx, parentID, facts.JobID, anchor.Envelope{WorkerRoundNotice: &facts})
}

func (m *Workers) AfterTerminal(
	ctx context.Context,
	parentID, completingJobID string,
	env anchor.Envelope,
) {
	if m == nil || strings.TrimSpace(parentID) == "" || strings.TrimSpace(completingJobID) == "" {
		return
	}
	m.Runtime.CoordinatorLoop().Nudges.NudgeAfterWorkerJobTerminal(
		ctx,
		parentID,
		completingJobID,
		anchor.WorkerTaskFinished,
		anchor.WorkerTaskFinished,
		"",
		env,
	)
}
