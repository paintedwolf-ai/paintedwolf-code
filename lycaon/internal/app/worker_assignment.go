package app

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (b *serveBuilder) composeWorkerAssignment(ctx context.Context, tctx tools.ToolContext, agentType string, brief wire.WorkerTaskCharter, task *wire.WorkerTask) (string, error) {
	msgs, err := b.storage.Sessions.GetMessages(ctx, tctx.Identity.SessionID)
	if err != nil {
		return brief.Goal, err
	}
	in := inject.WorkerTaskAssignmentInput{
		SessionID:        tctx.Identity.SessionID,
		ProjectDir:       tctx.ActiveRootPath(),
		Charter:          brief,
		AgentType:        agentType,
		WorkerJobID:      task.ID,
		MaxToolLoops:     task.MaxToolLoops,
		Attachments:      surface.SessionForwardedAttachments(msgs),
		RecordedVerdicts: recordedVerdictsForLeg(ctx, b.workflows.Manager, tctx.Identity.SessionID),
	}
	if task.Scope != nil {
		in.Scope = *task.Scope
	}
	run, err := b.workflows.Store.Runs.ActiveBySession(ctx, tctx.Identity.SessionID)
	if err != nil {
		return "", err
	}
	if run != nil {
		manifest, err := b.workflows.Manager.Resolver.ForRunID(ctx, run.ID)
		if err != nil {
			return "", err
		}
		in.CoverageAssignment, err = b.workflows.Manager.Assignments.TaskCoverageAssignment(ctx, task)
		if err != nil {
			return "", err
		}
		if def, ok := manifest.PhaseByID(run.CurrentPhase); ok && def.ReviewLoop != nil && def.ReviewLoop.IncludeScanInventory && (in.CoverageAssignment == nil || in.CoverageAssignment.QuestionID == "") {
			inventory, err := scan.WorkflowAdvisoryInventory(ctx, b.scanning.Store, run.ID)
			if err != nil {
				return "", err
			}
			in.ScanInventory = inventory
		}
	}
	return inject.RenderWorkerTaskAssignment(ctx, b.delegations.InjectRenderer, in)
}

// recordedVerdictsForLeg projects stamped verdicts onto the assignment DTO.
// The mapping lives here because inject cannot import workflow — workflow
// already imports inject.
func recordedVerdictsForLeg(ctx context.Context, mgr *workflow.RunManager, sessionID string) []inject.RecordedVerdict {
	stamped := mgr.Verdicts.StampedReviewVerdicts(ctx, sessionID)
	if len(stamped) == 0 {
		return nil
	}
	out := make([]inject.RecordedVerdict, 0, len(stamped))
	for _, v := range stamped {
		fields := make([]inject.RecordedVerdictField, 0, len(v.Fields))
		for _, f := range v.Fields {
			fields = append(fields, inject.RecordedVerdictField{Name: f.Name, Value: f.Value})
		}
		out = append(out, inject.RecordedVerdict{
			Phase:       v.Phase,
			EvidenceKey: v.EvidenceKey,
			Fields:      fields,
		})
	}
	return out
}
