package review

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

func rejectReviewView(reason, field string) error {
	return &toolrejection.ToolReject{Code: "WORKFLOW_REVIEW_VIEW_INVALID", Data: map[string]any{"reason": reason, "field": field}}
}

// View exposes bounded review context through the coordination board.
func (m *Assignments) View(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if m == nil || m.Runs == nil || m.WorkerTasks == nil {
		return "", rejectReviewView("review_unavailable", "review_view")
	}
	for _, key := range []string{"finding_id", "findings_after", "detail_level"} {
		if args[key] != nil {
			return "", rejectReviewView("exclusive_view", key)
		}
	}
	sessionID := tctx.Identity.SessionID
	if tctx.Identity.WorkerJobID != "" {
		sessionID = tctx.Identity.ParentSessionID
	}
	run, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if run == nil {
		return "", rejectReviewView("no_active_review", "review_view")
	}
	view, _ := args["review_view"].(string)
	id, _ := args["assignment_id"].(string)
	cursor, _ := args["cursor"].(string)
	if view == "subject" && id == "" || view != "subject" && id != "" {
		return "", rejectReviewView("subject_requires_assignment_id", "assignment_id")
	}
	if view == "summary" && cursor != "" {
		return "", rejectReviewView("summary_has_no_cursor", "cursor")
	}
	var out any
	if tctx.Identity.WorkerJobID != "" {
		if view != "subject" || id != tctx.Identity.WorkerJobID {
			return "", rejectReviewView("assigned_subject_only", "assignment_id")
		}
		tasks, err := m.WorkerTasks(ctx, run.ID)
		if err != nil {
			return "", err
		}
		allowed := false
		for _, task := range tasks {
			allowed = allowed || task.ID == id && task.ChildSessionID == tctx.Identity.SessionID
		}
		if !allowed {
			return "", rejectReviewView("assignment_not_bound_to_child", "assignment_id")
		}
	}
	switch view {
	case "summary":
		manifest, err := m.Resolver.ForRun(ctx, run)
		if err != nil {
			return "", err
		}
		phase, ok := manifest.PhaseByID(run.CurrentPhase)
		if !ok || phase.ReviewLoop == nil {
			return "", rejectReviewView("phase_not_review", "review_view")
		}
		vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
		if err != nil {
			return "", err
		}
		facts, err := m.Coverage.CoverageFacts(ctx, run, manifest)
		if err != nil {
			return "", err
		}
		agents, _ := runstate.EffectiveReviewAgents(run.CurrentPhase, *phase.ReviewLoop, vars)
		work := map[string]string{}
		for _, agent := range agents {
			work[agent] = reviewWorkID(agent)
		}
		questions, err := reviewQuestions(vars, run.CurrentPhase)
		if err != nil {
			return "", err
		}
		for i := range questions {
			questions[i].ReviewWorkID = questionReviewWorkID(questions[i].ID)
		}
		tasks, err := m.WorkerTasks(ctx, run.ID)
		if err != nil {
			return "", err
		}
		service := m
		subject, err := service.subjectFromFacts(ctx, run, manifest, *phase.ReviewLoop, facts)
		if err != nil {
			return "", err
		}
		prerequisite, err := service.validate(ctx, run, *phase.ReviewLoop, *subject, tasks)
		if err != nil {
			return "", err
		}
		var nextAction any
		if prerequisite != nil {
			nextAction = prerequisite.Data
		}
		out = map[string]any{"run_id": run.ID, "phase": run.CurrentPhase, "revision": facts.Revision, "work_ids": work, "questions": questions, "review_prerequisite": nextAction}
	case "assignments":
		bindings, err := m.Records.ReviewBindings(ctx, run.ID, run.CurrentPhase, cursor, 50)
		if err != nil {
			return "", err
		}
		rows := make([]map[string]any, 0, len(bindings))
		for _, b := range bindings {
			rows = append(rows, map[string]any{"assignment_id": b.ID, "work_id": b.WorkID, "agent": b.Agent, "purpose": b.Purpose, "job_status": b.JobStatus, "revision": b.Subject.Facts.Revision, "predecessor_job_ids": b.PredecessorJobs})
		}
		next := ""
		if len(bindings) == 50 {
			next = bindings[len(bindings)-1].ID
		}
		out = map[string]any{"assignments": rows, "next_cursor": next}
	case "subject":
		binding, err := m.Records.ReviewBinding(ctx, id)
		if err != nil {
			return "", err
		}
		if binding == nil || binding.RunID != run.ID {
			return "", rejectReviewView("assignment_unavailable", "assignment_id")
		}
		offset := 0
		if cursor != "" {
			offset, err = strconv.Atoi(cursor)
			if err != nil || offset < 0 {
				return "", rejectReviewView("invalid_cursor", "cursor")
			}
		}
		page, next, err := reviewcoverage.Page(binding.Subject, offset, reviewcoverage.SubjectPageSize)
		if err != nil {
			return "", rejectReviewView("invalid_cursor", "cursor")
		}
		facts := page.Facts.Obligations
		facts = append(facts, page.Facts.Gaps...)
		out = map[string]any{"assignment_id": id, "purpose": binding.Purpose, "coverage_required": binding.CoverageRequired, "revision": binding.Subject.Facts.Revision, "facts": facts, "candidate": page.Candidate, "next_cursor": next}
	default:
		return "", rejectReviewView("unknown_view", "review_view")
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}
