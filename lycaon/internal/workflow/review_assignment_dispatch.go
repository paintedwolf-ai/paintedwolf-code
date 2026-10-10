package workflow

import (
	"context"
	"net/url"
	"slices"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/pkg/api"
)

func reviewWorkID(agent string) string { return "review/" + url.PathEscape(agent) }

type reviewAssignments struct{ runs *RunManager }

func (r reviewAssignments) bind(ctx context.Context, run *api.WorkflowRun, task *api.WorkerTask) error {
	m := r.runs
	manifest, err := m.manifestForRun(ctx, run)
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
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
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
	if binding.QuestionID == "" && !slices.Contains(dedupeReviewAgents(def.RequiredAgents, def.IfSpawnable), task.AgentType) {
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
				priorBinding, err := m.Store.ReviewBinding(ctx, prior.ID)
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
	return m.Store.RecordReviewBinding(ctx, run, binding)
}

// TaskCoverageAssignment reads the exact context recorded before prompt composition.
func (m *RunManager) TaskCoverageAssignment(ctx context.Context, task *api.WorkerTask) (*reviewcoverage.Binding, error) {
	binding, err := m.Store.ReviewBinding(ctx, task.ID)
	if err != nil || binding == nil {
		return binding, err
	}
	if binding.RunID != task.WorkflowRunID || binding.Phase != task.WorkflowPhase || binding.Agent != task.AgentType || binding.WorkID != task.WorkflowWorkID {
		return nil, rejectFanoutTask("review_assignment_mismatch", task)
	}
	return binding, nil
}

func (r reviewAssignments) assertInitial(ctx context.Context, run *api.WorkflowRun, task *api.WorkerTask) error {
	if r.runs.WorkerTasks == nil {
		return nil
	}
	tasks, err := r.runs.WorkerTasks(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, prior := range tasks {
		if prior.ID == task.ID || prior.SourceToolCallID != "" && prior.SourceToolCallID == task.SourceToolCallID {
			continue
		}
		if prior.WorkflowPhase == task.WorkflowPhase && prior.AgentType == task.AgentType && prior.WorkflowWorkID == task.WorkflowWorkID && !prior.Status.IsTerminal() {
			return rejectFanoutTask("review_assignment_already_active", task)
		}
	}
	return nil
}
