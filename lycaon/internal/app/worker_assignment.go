package app

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/app/boards"
	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/app/scanning"
	"github.com/lycaon/lycaon/internal/app/workerruntime"
	"github.com/lycaon/lycaon/internal/app/workflows"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (b *serveBuilder) composeWorkerAssignment(ctx context.Context, tctx tools.ToolContext, agentType string, brief wire.WorkerTaskCharter, task *wire.WorkerTask) (string, error) {
	return composeWorkerTaskAssignment(ctx, b.storage, b.workflows, b.scanning, b.boards, b.workerRuntime, tctx, agentType, brief, task)
}

func composeWorkerTaskAssignment(
	ctx context.Context,
	storage persistence.Runtime,
	wf *workflows.Runtime,
	scanningRT *scanning.Runtime,
	boardsRT *boards.Runtime,
	workersRt *workerruntime.Runtime,
	tctx tools.ToolContext,
	agentType string,
	brief wire.WorkerTaskCharter,
	task *wire.WorkerTask,
) (string, error) {
	msgs, err := storage.Sessions.GetMessages(ctx, tctx.Identity.SessionID)
	if err != nil {
		return brief.Goal, err
	}
	in := inject.WorkerTaskAssignmentInput{
		SessionID:        tctx.Identity.SessionID,
		ProjectDir:       tctx.ActiveRootPath(),
		Charter:          brief,
		AgentType:        agentType,
		WorkerJobID:      task.ID,
		Attachments:      surface.SessionForwardedAttachments(msgs),
		RecordedVerdicts: recordedVerdictsForLeg(ctx, wf.Manager, tctx.Identity.SessionID),
	}
	if task.Scope != nil {
		in.Scope = *task.Scope
	}
	if child := strings.TrimSpace(task.ChildSessionID); child != "" {
		in.RoundInstructions = strings.TrimSpace(task.Prompt)
		if boardsRT != nil && boardsRT.WorkerRecords != nil {
			if w, wErr := boardsRT.WorkerRecords.Self(ctx, workerrecords.Actor{SessionID: child, Role: workerrecords.RoleWorker, PrincipalID: child}); wErr == nil {
				in.WorkerName = w.Name
				if workersRt != nil && workersRt.Workspace != nil && workersRt.Workspace.BranchBase() != "" {
					in.ViewKind = workspace.BranchViewKind(filepath.Join(workersRt.Workspace.BranchBase(), w.ID))
				}
			}
		}
	}
	run, err := wf.Store.Runs.ActiveBySession(ctx, tctx.Identity.SessionID)
	if err != nil {
		return "", err
	}
	if run != nil {
		manifest, err := wf.Manager.Resolver.ForRunID(ctx, run.ID)
		if err != nil {
			return "", err
		}
		in.CoverageAssignment, err = wf.Manager.Assignments.TaskCoverageAssignment(ctx, task)
		if err != nil {
			return "", err
		}
		if def, ok := manifest.PhaseByID(run.CurrentPhase); ok && def.ReviewLoop != nil && def.ReviewLoop.IncludeScanInventory && (in.CoverageAssignment == nil || in.CoverageAssignment.QuestionID == "") {
			inventory, err := scan.WorkflowAdvisoryInventory(ctx, scanningRT.Store.Reads, run.ID)
			if err != nil {
				return "", err
			}
			in.ScanInventory = inventory
		}
	}
	return inject.RenderWorkerTaskAssignment(ctx, workersRt.InjectRenderer, in)
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
