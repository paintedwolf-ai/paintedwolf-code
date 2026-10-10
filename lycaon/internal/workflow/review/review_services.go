package review

import (
	"context"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type Coverage struct {
	Assignments         *Assignments
	Reviews             workflowpresentation.ReviewEvidenceLister
	Runs                runstate.RunsRepository
	Resolver            *catalog.Resolver
	Inventory           ScanInventory
	WorkerTasks         func(context.Context, string) ([]api.WorkerTask, error)
	inventoryShortfalls inventoryShortfalls
}
type Questions struct {
	Assignments *Assignments
	Runs        runstate.RunsRepository
	Resolver    *catalog.Resolver
	Coverage    *Coverage
	Verdicts    *Verdicts
	WorkerTasks func(context.Context, string) ([]api.WorkerTask, error)
}
type Verdicts struct {
	Assignments             *Assignments
	Evidence                *ReviewEvidence
	Runs                    runstate.RunsRepository
	Records                 runstate.VerdictsRepository
	Vars                    *runstate.Variables
	Resolver                *catalog.Resolver
	Sessions                session.Store
	Phases                  *workflowphases.Service
	Coverage                *Coverage
	Questions               *Questions
	EvidenceStore           inspector.EvidenceStore
	EvidenceProjectDir      func(context.Context, string) (string, error)
	WorkerTasks             func(context.Context, string) ([]api.WorkerTask, error)
	VerdictGrounding        func(context.Context, string, []api.CitationGroundingCitedEvidence, []string, []string) (guidance.VerdictGroundingEval, error)
	OnGateEvidencePersisted func(context.Context, string, string, evidence.Record)
	OnReviewLoopHeld        ReviewLoopHeldHook
}

// ReviewLoopHeldHook reports a held review and whether its cap requires a terminal verdict.
type ReviewLoopHeldHook func(ctx context.Context, sessionID string, decisionRequired bool)

type Assignments struct {
	Runs        runstate.RunsRepository
	Records     runstate.AssignmentsRepository
	Resolver    *catalog.Resolver
	Coverage    *Coverage
	Verdicts    *Verdicts
	WorkerTasks func(context.Context, string) ([]api.WorkerTask, error)
	Snapshot    func(context.Context, *api.WorkflowRun, workflowdef.Manifest, map[string]any) runstate.ReviewSnapshot
}
