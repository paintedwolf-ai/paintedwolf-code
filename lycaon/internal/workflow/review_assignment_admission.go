package workflow

import (
	"context"
	"slices"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

const ReviewRequiredCode = "SUBMIT_VERDICT_REVIEW_REQUIRED"
const ReviewContextChangedCode = "SUBMIT_VERDICT_REVIEW_CONTEXT_CHANGED"

type assessedAssignment struct {
	binding reviewcoverage.Binding
	review  api.CoverageReview
}

func (r reviewAssignments) completed(ctx context.Context, runID, phase, agent string, tasks []api.WorkerTask) ([]assessedAssignment, []string, error) {
	var out []assessedAssignment
	var active []string
	for _, task := range tasks {
		if task.WorkflowRunID != runID || task.WorkflowPhase != phase || task.AgentType != agent {
			continue
		}
		binding, err := r.runs.Store.ReviewBinding(ctx, task.ID)
		if err != nil {
			return nil, nil, err
		}
		if binding == nil || binding.Purpose == reviewcoverage.QuestionInvestigation {
			continue
		}
		if !task.Status.IsTerminal() {
			if binding.Purpose == reviewcoverage.IndependentReview {
				active = append(active, task.ID)
			}
			continue
		}
		if !api.WorkerReviewSucceeded(task) || task.Result == nil || task.Result.CompletionReport == nil {
			continue
		}
		var review api.CoverageReview
		if binding.CoverageRequired {
			if task.Result.CompletionReport.CoverageReview == nil {
				continue
			}
			review = *task.Result.CompletionReport.CoverageReview
			if err := reviewcoverage.Validate(binding.Subject.Facts, review); err != nil {
				continue
			}
		}
		if binding.Purpose == reviewcoverage.QuestionReview && slices.ContainsFunc(tasks, func(t api.WorkerTask) bool {
			return t.WorkflowPhase == phase && t.WorkflowWorkID == binding.QuestionID && api.WorkerReviewSucceeded(t) && !slices.Contains(binding.InvestigationJobs, t.ID)
		}) {
			continue
		}
		out = append(out, assessedAssignment{*binding, review})
	}
	return out, active, nil
}

// applicableReview retains a base assessment and checks scoped successor inputs.
// The coordinator adjudicates conclusions; applicability never infers agreement.
func applicableReview(base assessedAssignment, updates []assessedAssignment, current reviewcoverage.Assignment) bool {
	changed := reviewcoverage.ChangedItems(base.binding.Subject, current)
	for _, update := range updates {
		if update.binding.Purpose != reviewcoverage.QuestionReview || !slices.Contains(update.binding.PredecessorJobs, base.binding.ID) {
			continue
		}
		scope := reviewcoverage.Scoped(current, reviewObligationIDs(update.binding.Subject.Facts), update.binding.ClaimIDs...)
		if len(reviewcoverage.ChangedItems(update.binding.Subject, scope)) != 0 {
			continue
		}
		// Compose also proves the successor cannot silently add unrelated items.
		if base.binding.CoverageRequired {
			if _, err := reviewcoverage.Compose(base.review, update.review, update.binding.Subject.Facts); err != nil {
				continue
			}
		}
		outsideScope := reviewcoverage.ChangedItems(current, scope)
		changed = slices.DeleteFunc(changed, func(id string) bool {
			if slices.Contains(outsideScope, id) {
				return false
			}
			for _, claim := range update.binding.ClaimIDs {
				if id == "claim/"+claim {
					return true
				}
			}
			for _, rows := range [][]reviewcoverage.Fact{scope.Facts.Obligations, scope.Facts.Gaps} {
				for _, f := range rows {
					if f.ID == id {
						return true
					}
				}
			}
			return false
		})
	}
	return len(changed) == 0
}

func (r reviewAssignments) validate(ctx context.Context, run *api.WorkflowRun, def workflowdef.ReviewLoopDef, current reviewcoverage.Assignment, tasks []api.WorkerTask) (*tools.ToolReject, error) {
	agents := def.CoverageReviewers
	if def.AssignmentBinding == "explicit" {
		vars, err := r.runs.Store.GetScaffoldVars(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		agents, _ = effectiveReviewAgents(run.CurrentPhase, def, vars)
	}
	for _, agent := range agents {
		completed, active, err := r.completed(ctx, run.ID, run.CurrentPhase, agent, tasks)
		if err != nil {
			return nil, err
		}
		accepted := slices.ContainsFunc(completed, func(a assessedAssignment) bool {
			return a.binding.Purpose == reviewcoverage.IndependentReview && applicableReview(a, completed, current)
		})
		if !accepted {
			action := "dispatch_work"
			if len(active) > 0 {
				action = "wait_for_work"
			}
			return &tools.ToolReject{Code: ReviewRequiredCode, Data: map[string]any{"action": action, "work_ids": []string{reviewWorkID(agent)}, "agent": agent, "job_ids": active}}, nil
		}
	}
	vars, err := r.runs.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	questions, err := reviewQuestions(vars, run.CurrentPhase)
	if err != nil {
		return nil, err
	}
	for _, q := range questions {
		count, _ := questionAttempts(tasks, run.CurrentPhase, q.ID)
		if count == 0 || questionBlocked(tasks, run.CurrentPhase, q.ID) {
			continue
		}
		for _, agent := range def.CoverageReviewers {
			valid, err := r.questionCurrent(ctx, run, def, tasks, q.ID, agent)
			if err != nil {
				return nil, err
			}
			if !valid {
				action := "dispatch_work"
				var active []string
				for _, task := range tasks {
					if task.WorkflowPhase == run.CurrentPhase && task.AgentType == agent && task.WorkflowWorkID == q.ID+"/review" && !task.Status.IsTerminal() {
						active = append(active, task.ID)
					}
				}
				if len(active) > 0 {
					action = "wait_for_work"
				}
				return &tools.ToolReject{Code: ReviewRequiredCode, Data: map[string]any{"action": action, "work_ids": []string{q.ID + "/review"}, "agent": agent, "job_ids": active}}, nil
			}
		}
	}

	return nil, nil
}

func reviewObligationIDs(facts reviewcoverage.Facts) []string {
	out := make([]string, 0, len(facts.Obligations))
	for _, f := range facts.Obligations {
		out = append(out, f.ID)
	}
	return out
}

func (r reviewAssignments) questionCurrent(ctx context.Context, run *api.WorkflowRun, def workflowdef.ReviewLoopDef, tasks []api.WorkerTask, question, agent string) (bool, error) {
	completed, _, err := r.completed(ctx, run.ID, run.CurrentPhase, agent, tasks)
	if err != nil {
		return false, err
	}
	manifest, err := r.runs.manifestForRun(ctx, run)
	if err != nil {
		return false, err
	}
	current, err := r.subject(ctx, run, manifest, def)
	if err != nil {
		return false, err
	}
	for _, a := range completed {
		if a.binding.Purpose != reviewcoverage.QuestionReview || a.binding.QuestionID != question {
			continue
		}
		scoped := reviewcoverage.Scoped(*current, reviewObligationIDs(a.binding.Subject.Facts), a.binding.ClaimIDs...)
		if len(reviewcoverage.ChangedItems(a.binding.Subject, scoped)) == 0 {
			return true, nil
		}
	}
	return false, nil
}
