package review

import (
	"context"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"
	"net/url"
	"slices"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/pkg/api"
)

func reviewWorkID(agent string) string { return "review/" + url.PathEscape(agent) }

func (r Assignments) Bind(ctx context.Context, run *api.WorkflowRun, task *api.WorkerTask) error {
	m := r
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return err
	}
	phase, ok := manifest.PhaseByID(task.WorkflowPhase)
	if !ok || phase.ReviewLoop == nil {
		return nil
	}
	def := *phase.ReviewLoop
	if len(def.CoverageReviewers) == 0 && def.FollowupAttempts == 0 && def.AssignmentBinding != "explicit" {
		return nil
	}
	binding := reviewcoverage.Binding{ID: task.ID, RunID: run.ID, Phase: task.WorkflowPhase, WorkID: task.WorkflowWorkID, Agent: task.AgentType, Purpose: reviewcoverage.IndependentReview, CoverageRequired: slices.Contains(def.CoverageReviewers, task.AgentType)}
	vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return err
	}
	questions, err := reviewQuestions(vars, run.CurrentPhase)
	if err != nil {
		return err
	}
	var scope []string
	for _, q := range questions {
		if task.WorkflowWorkID != q.ID && task.WorkflowWorkID != questionReviewWorkID(q.ID) {
			continue
		}
		binding.QuestionID, binding.ClaimIDs = q.ID, []string{q.ClaimID}
		scope = q.Obligations
		if task.WorkflowWorkID == q.ID {
			binding.Purpose = reviewcoverage.QuestionInvestigation
			binding.CoverageRequired = false
		} else {
			binding.Purpose = reviewcoverage.QuestionReview
		}
	}
	if binding.QuestionID == "" && !slices.Contains(runstate.DedupeReviewAgents(def.RequiredAgents, def.IfSpawnable), task.AgentType) {
		return nil
	}
	subject, err := r.subject(ctx, run, manifest, def)
	if err != nil {
		return err
	}
	binding.Subject = *subject
	if binding.QuestionID != "" {
		binding.Subject = reviewcoverage.Scoped(*subject, scope, binding.ClaimIDs...)
		tasks, err := m.WorkerTasks(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, prior := range tasks {
			if prior.WorkflowPhase != task.WorkflowPhase || !api.WorkerReviewSucceeded(prior) {
				continue
			}
			if prior.WorkflowWorkID == binding.QuestionID {
				binding.InvestigationJobs = append(binding.InvestigationJobs, prior.ID)
			}
			if prior.AgentType == task.AgentType {
				priorBinding, err := m.Records.ReviewBinding(ctx, prior.ID)
				if err != nil {
					return err
				}
				if priorBinding != nil && (priorBinding.Purpose == reviewcoverage.IndependentReview || priorBinding.QuestionID == binding.QuestionID && priorBinding.Purpose == reviewcoverage.QuestionReview) {
					binding.PredecessorJobs = append(binding.PredecessorJobs, prior.ID)
				}
			}
		}
		slices.Sort(binding.PredecessorJobs)
		slices.Sort(binding.InvestigationJobs)
		if binding.Purpose == reviewcoverage.QuestionReview {
			task.AfterWorkers = append(task.AfterWorkers, binding.InvestigationJobs...)
			slices.Sort(task.AfterWorkers)
			task.AfterWorkers = slices.Compact(task.AfterWorkers)
		}
	}
	return m.Records.RecordReviewBinding(ctx, run, binding)
}

// TaskCoverageAssignment reads the exact context recorded before prompt composition.
func (m *Assignments) TaskCoverageAssignment(ctx context.Context, task *api.WorkerTask) (*reviewcoverage.Binding, error) {
	binding, err := m.Records.ReviewBinding(ctx, task.ID)
	if err != nil || binding == nil {
		return binding, err
	}
	if binding.RunID != task.WorkflowRunID || binding.Phase != task.WorkflowPhase || binding.Agent != task.AgentType || binding.WorkID != task.WorkflowWorkID {
		return nil, toolguard.RejectFanoutTask("review_assignment_mismatch", task)
	}
	return binding, nil
}

func (r Assignments) AssertInitial(ctx context.Context, run *api.WorkflowRun, task *api.WorkerTask) error {
	if r.WorkerTasks == nil {
		return nil
	}
	tasks, err := r.WorkerTasks(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, prior := range tasks {
		if prior.ID == task.ID || prior.SourceToolCallID != "" && prior.SourceToolCallID == task.SourceToolCallID {
			continue
		}
		if prior.WorkflowPhase == task.WorkflowPhase && prior.AgentType == task.AgentType && prior.WorkflowWorkID == task.WorkflowWorkID && !prior.Status.IsTerminal() {
			return toolguard.RejectFanoutTask("review_assignment_already_active", task)
		}
	}
	return nil
}
