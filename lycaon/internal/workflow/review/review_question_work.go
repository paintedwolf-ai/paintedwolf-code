package review

import (
	"context"
	"fmt"
	toolguard "github.com/lycaon/lycaon/internal/workflow/toolguard"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

func checkQuestionClosure(def workflowdef.ReviewLoopDef, claims []workflowvalidation.VerdictClaim, questions []reviewQuestionWork, tasks []api.WorkerTask, phase string, review *api.CoverageReview) error {
	if review == nil {
		return rejectReviewQuestion("coverage_required", "")
	}
	for _, q := range questions {
		index := slices.IndexFunc(claims, func(c workflowvalidation.VerdictClaim) bool { return c.ID == q.ClaimID })
		if index < 0 {
			return rejectReviewQuestion("claim_outcome_required", q.ID)
		}
		completed, active := questionAttempts(tasks, phase, q.ID)
		_, reviewing := questionAttempts(tasks, phase, q.ID+"/review")
		if active || reviewing {
			return rejectReviewQuestion("work_active", q.ID)
		}
		open := def.ClassOf(claims[index].Status) == workflowdef.ClaimOpen
		var disposition string
		if open {
			a := slices.IndexFunc(review.Assessments, func(a api.CoverageAssessment) bool { return a.ID == q.ID })
			if a < 0 {
				return rejectReviewQuestion("assessment_required", q.ID)
			}
			disposition = review.Assessments[a].Disposition
			if disposition == reviewcoverage.Covered {
				return rejectReviewQuestion("open_question_covered", q.ID)
			}
			if disposition == reviewcoverage.EssentialOpen && questionBlocked(tasks, phase, q.ID) {
				continue
			}
		}
		if completed > 0 && !questionReviewed(tasks, phase, q.ID, def.RequiredAgents) {
			return rejectReviewQuestion("current_review_required", q.ID)
		}
		if !open {
			continue
		}
		if disposition == reviewcoverage.Immaterial {
			continue
		}
		if completed < def.FollowupAttempts {
			return rejectReviewQuestion("investigation_required", q.ID)
		}
	}
	return nil
}

func questionAttempts(tasks []api.WorkerTask, phase, id string) (int, bool) {
	completed, active := 0, false
	for _, task := range tasks {
		if task.WorkflowPhase != phase || task.WorkflowWorkID != id {
			continue
		}
		switch task.Status {
		case api.WorkerStatusComplete:
			completed++
		case api.WorkerStatusFailed, api.WorkerStatusCanceled:
		default:
			active = true
		}
	}
	return completed, active
}

func (m *Questions) AssertTask(ctx context.Context, run *api.WorkflowRun, def workflowdef.ReviewLoopDef, vars map[string]any, task *api.WorkerTask) error {
	if task.EffectiveScope().Mode != "read" {
		return toolguard.RejectFanoutTask("review_question_requires_read_scope", task)
	}
	questions, err := reviewQuestions(vars, run.CurrentPhase)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(questions, func(q reviewQuestionWork) bool {
		return q.ID == task.WorkflowWorkID || q.ID+"/review" == task.WorkflowWorkID
	}) {
		return toolguard.RejectFanoutTask("unknown_review_question", task)
	}
	if m.WorkerTasks == nil {
		return fmt.Errorf("review worker ledger unavailable")
	}
	tasks, err := m.WorkerTasks(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, prior := range tasks {
		if prior.SourceToolCallID != "" && prior.SourceToolCallID == task.SourceToolCallID && prior.ParentSessionID == task.ParentSessionID {
			return nil
		}
		if task.ChildSessionID != "" && prior.ChildSessionID == task.ChildSessionID && prior.WorkflowWorkID != task.WorkflowWorkID {
			return toolguard.RejectFanoutTask("recovery_child_belongs_to_another_question", task)
		}
	}
	if strings.HasSuffix(task.WorkflowWorkID, "/review") {
		if !slices.Contains(def.RequiredAgents, task.AgentType) {
			return toolguard.RejectFanoutTask("review_question_requires_declared_reviewer", task)
		}
		questionID := strings.TrimSuffix(task.WorkflowWorkID, "/review")
		completed, active := questionAttempts(tasks, run.CurrentPhase, questionID)
		if active {
			return toolguard.RejectFanoutTask("review_question_investigation_active", task)
		}
		if completed == 0 {
			return toolguard.RejectFanoutTask("review_question_investigation_required", task)
		}
		if questionReviewed(tasks, run.CurrentPhase, questionID, []string{task.AgentType}) {
			return toolguard.RejectFanoutTask("review_question_already_reviewed", task)
		}
	}
	if strings.HasSuffix(task.WorkflowWorkID, "/review") {
		tasks = slices.DeleteFunc(slices.Clone(tasks), func(prior api.WorkerTask) bool { return prior.AgentType != task.AgentType })
	}
	completed, active := questionAttempts(tasks, run.CurrentPhase, task.WorkflowWorkID)
	if active {
		return toolguard.RejectFanoutTask("review_question_already_active", task)
	}
	if !strings.HasSuffix(task.WorkflowWorkID, "/review") && completed >= def.FollowupAttempts {
		return toolguard.RejectFanoutTask("review_question_attempts_exhausted", task)
	}
	return nil
}

func questionReviewed(tasks []api.WorkerTask, phase, id string, agents []string) bool {
	var latest time.Time
	for _, task := range tasks {
		if task.WorkflowPhase != phase || task.WorkflowWorkID != id || task.Status != api.WorkerStatusComplete {
			continue
		}
		end := task.CreatedAt
		if task.CompletedAt != nil {
			end = *task.CompletedAt
		}
		if end.After(latest) {
			latest = end
		}
	}
	for _, agent := range agents {
		found := false
		for _, task := range tasks {
			if task.WorkflowPhase == phase && task.WorkflowWorkID == id+"/review" && task.AgentType == agent && api.WorkerReviewSucceeded(task) && !task.CreatedAt.Before(latest) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func questionBlocked(tasks []api.WorkerTask, phase, id string) bool {
	latest := map[string]api.WorkerTask{}
	for _, task := range tasks {
		if task.WorkflowPhase != phase || (task.WorkflowWorkID != id && task.WorkflowWorkID != id+"/review") {
			continue
		}
		key := task.WorkflowWorkID
		if key == id+"/review" {
			key += "/" + task.AgentType
		}
		prior, exists := latest[key]
		if !exists || !task.CreatedAt.Before(prior.CreatedAt) {
			latest[key] = task
		}
	}
	for _, task := range latest {
		if task.Status == api.WorkerStatusFailed || task.Status == api.WorkerStatusCanceled || (task.WorkflowWorkID == id+"/review" && task.Status == api.WorkerStatusComplete && !api.WorkerReviewSucceeded(task)) {
			return true
		}
	}
	return false
}

func questionCoverageFact(q reviewQuestionWork, tasks []api.WorkerTask, phase workflowdef.PhaseDef) reviewcoverage.Fact {
	completed, active := questionAttempts(tasks, phase.ID, q.ID)
	fact := reviewcoverage.Fact{ID: q.ID, Kind: "review_question", Subject: q.ClaimID, Question: q.MissingFact, Obligations: q.Obligations, InvestigationAttempts: completed, FollowupLimit: phase.ReviewLoop.FollowupAttempts, InvestigationActive: active, ReviewRequired: completed > 0 && !questionReviewed(tasks, phase.ID, q.ID, phase.ReviewLoop.RequiredAgents)}
	for _, task := range tasks {
		if task.WorkflowPhase == phase.ID && (task.WorkflowWorkID == q.ID || task.WorkflowWorkID == q.ID+"/review") {
			fact.Tasks = append(fact.Tasks, task.ID)
		}
	}
	slices.Sort(fact.Tasks)
	return fact
}
