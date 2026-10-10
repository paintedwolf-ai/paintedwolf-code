package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReviewAssignmentsView exposes bounded review context through the coordination board.
func ReviewAssignmentsView(ctx context.Context, m *RunManager, args map[string]any, tctx tools.ToolContext) (string, error) {
	for _, key := range []string{"finding_id", "findings_after", "detail_level"} {
		if args[key] != nil {
			return "", &tools.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "pack_board", "field": key, "reason": "review view is exclusive"}}
		}
	}
	sessionID := tctx.SessionID
	if tctx.WorkerJobID != "" {
		sessionID = tctx.ParentSessionID
	}
	run, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if run == nil {
		return "", fmt.Errorf("no active workflow review")
	}
	view, _ := args["review_view"].(string)
	id, _ := args["assignment_id"].(string)
	cursor, _ := args["cursor"].(string)
	var out any
	if tctx.WorkerJobID != "" {
		if view != "subject" || id != tctx.WorkerJobID {
			return "", fmt.Errorf("worker review view requires its assigned subject")
		}
		tasks, err := m.WorkerTasks(ctx, run.ID)
		if err != nil {
			return "", err
		}
		allowed := false
		for _, task := range tasks {
			allowed = allowed || task.ID == id && task.ChildSessionID == tctx.SessionID
		}
		if !allowed {
			return "", fmt.Errorf("review assignment is not bound to this child")
		}
	}
	switch view {
	case "summary":
		manifest, err := m.manifestForRun(ctx, run)
		if err != nil {
			return "", err
		}
		phase, ok := manifest.PhaseByID(run.CurrentPhase)
		if !ok || phase.ReviewLoop == nil {
			return "", fmt.Errorf("current phase is not a review")
		}
		vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
		if err != nil {
			return "", err
		}
		facts, err := m.CoverageFacts(ctx, run, manifest)
		if err != nil {
			return "", err
		}
		agents, _ := effectiveReviewAgents(run.CurrentPhase, *phase.ReviewLoop, vars)
		work := map[string]string{}
		for _, agent := range agents {
			work[agent] = reviewWorkID(agent)
		}
		questions, err := reviewQuestions(vars, run.CurrentPhase)
		if err != nil {
			return "", err
		}
		for i := range questions {
			questions[i].ReviewWorkID = questions[i].ID + "/review"
		}
		tasks, err := m.WorkerTasks(ctx, run.ID)
		if err != nil {
			return "", err
		}
		service := reviewAssignments{m}
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
		bindings, err := m.Store.ReviewBindings(ctx, run.ID, run.CurrentPhase, cursor, 50)
		if err != nil {
			return "", err
		}
		rows := make([]map[string]any, 0, len(bindings))
		for _, b := range bindings {
			rows = append(rows, map[string]any{"assignment_id": b.ID, "work_id": b.WorkID, "agent": b.Agent, "purpose": b.Purpose, "revision": b.Subject.Facts.Revision, "predecessor_job_ids": b.PredecessorJobs})
		}
		next := ""
		if len(bindings) == 50 {
			next = bindings[len(bindings)-1].ID
		}
		out = map[string]any{"assignments": rows, "next_cursor": next}
	case "subject":
		binding, err := m.Store.ReviewBinding(ctx, id)
		if err != nil {
			return "", err
		}
		if binding == nil || binding.RunID != run.ID {
			return "", fmt.Errorf("review assignment unavailable in this run")
		}
		offset := 0
		if cursor != "" {
			offset, err = strconv.Atoi(cursor)
			if err != nil || offset < 0 {
				return "", fmt.Errorf("invalid subject cursor")
			}
		}
		facts := append(append([]reviewcoverage.Fact{}, binding.Subject.Facts.Obligations...), binding.Subject.Facts.Gaps...)
		if offset > len(facts) {
			return "", fmt.Errorf("subject cursor exceeds scope")
		}
		end := min(offset+50, len(facts))
		next := ""
		if end < len(facts) {
			next = strconv.Itoa(end)
		}
		out = map[string]any{"assignment_id": id, "purpose": binding.Purpose, "coverage_required": binding.CoverageRequired, "revision": binding.Subject.Facts.Revision, "facts": facts[offset:end], "candidate": reviewCandidatePage(binding.Subject.Candidate, facts[offset:end]), "next_cursor": next}
	default:
		return "", fmt.Errorf("unknown review view")
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}

func reviewCandidatePage(candidate api.CoverageReview, facts []reviewcoverage.Fact) api.CoverageReview {
	page := api.CoverageReview{Revision: candidate.Revision, Assessments: []api.CoverageAssessment{}}
	for _, assessment := range candidate.Assessments {
		for _, fact := range facts {
			if assessment.ID == fact.ID {
				page.Assessments = append(page.Assessments, assessment)
				break
			}
		}
	}
	return page
}
