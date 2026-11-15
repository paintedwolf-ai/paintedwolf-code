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
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
)

type Workers struct {
	Runtime    *coordinator.Runtime
	Batch      *batchcontrol.Service
	Settlement *turnsettlement.Service
	Admission  *turnadmission.Service
	Digests    *workeroutcomes.Digests
	Results    *workeroutcomes.Results
	// Names names the worker a round notice is about.
	Names workercompletion.WorkerNames
	// LatestSent is the seq of the latest message the worker running a job
	// sent to its coordinator; zero when it has sent none.
	LatestSent func(ctx context.Context, jobID string) (int64, error)
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
	facts = m.addressed(ctx, facts)
	m.Runtime.CoordinatorLoop().Nudges.NudgeWorkerRoundNotice(ctx, parentID, facts.JobID, anchor.Envelope{WorkerRoundNotice: &facts})
}

// addressed adds what the coordinator knows the round's worker by: its name
// and its latest message.
func (m *Workers) addressed(ctx context.Context, facts kick.WorkerRoundNoticeFacts) kick.WorkerRoundNoticeFacts {
	facts.Worker = m.Names.Name(ctx, facts.JobID)
	if m.LatestSent != nil {
		if seq, err := m.LatestSent(ctx, facts.JobID); err == nil {
			facts.LatestMessageSeq = seq
		}
	}
	return facts
}

// PrerequisiteBlocked tells a coordinator that a waiting worker's
// prerequisite can no longer be met, naming both workers and why.
func (m *Workers) PrerequisiteBlocked(ctx context.Context, parentID, consumerJobID, producerJobID, reason string) {
	parentID = strings.TrimSpace(parentID)
	if m == nil || parentID == "" || strings.TrimSpace(consumerJobID) == "" {
		return
	}
	env := anchor.Envelope{Vars: map[string]any{
		"worker": m.Names.Name(ctx, consumerJobID), "producer": m.Names.Name(ctx, producerJobID), "producer_state": reason,
	}}
	m.Runtime.CoordinatorLoop().Nudges.NudgeWorkerPrerequisiteBlocked(ctx, parentID, consumerJobID, env)
}

// MessageNeedsReply wakes a coordinator for a running worker's message that
// asks for an answer.
func (m *Workers) MessageNeedsReply(ctx context.Context, parentID, jobID string) {
	parentID = strings.TrimSpace(parentID)
	if m == nil || parentID == "" || strings.TrimSpace(jobID) == "" {
		return
	}
	m.Runtime.CoordinatorLoop().Nudges.NudgeWorkerMessage(ctx, parentID, strings.TrimSpace(jobID))
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
