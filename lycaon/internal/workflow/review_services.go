package workflow

import (
	"context"
	"fmt"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *RunManager) buildReviewDomains(store *runstate.Repository) *workflowreview.Questions {
	m.Coverage = &workflowreview.Coverage{Runs: store.Runs, Resolver: &m.Resolver}
	questions := &workflowreview.Questions{Runs: store.Runs, Resolver: &m.Resolver, Coverage: m.Coverage}
	m.Verdicts = &workflowreview.Verdicts{Runs: store.Runs, Records: store.Verdicts, Vars: m.Vars, Resolver: &m.Resolver, Sessions: m.Sessions, Coverage: m.Coverage, Questions: questions}
	m.Repairs = &ReviewRepairs{RunManager: m}
	m.Assignments = &workflowreview.Assignments{
		Runs: store.Runs, Records: store.Assignments, Resolver: &m.Resolver,
		Coverage: m.Coverage, Verdicts: m.Verdicts,
		WorkerTasks: func(ctx context.Context, runID string) ([]api.WorkerTask, error) {
			if m.Coverage.WorkerTasks == nil {
				return nil, fmt.Errorf("workflow worker ledger unavailable")
			}
			return m.Coverage.WorkerTasks(ctx, runID)
		},
		Snapshot: func(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, vars map[string]any) runstate.ReviewSnapshot {
			return m.Repairs.CaptureSnapshot(ctx, run, manifest, vars, nil)
		},
	}
	m.Coverage.Assignments = m.Assignments
	m.Verdicts.Assignments = m.Assignments
	questions.Assignments = m.Assignments
	questions.Verdicts = m.Verdicts
	m.Coverage.Reviews = m.Verdicts
	return questions
}
