package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReviewQuestion binds a missing fact to affected coverage obligations.
type ReviewQuestion struct {
	MissingFact string   `json:"missing_fact"`
	Obligations []string `json:"obligations"`
}

type reviewQuestionWork struct {
	ReviewWorkID string `json:"review_work_id,omitempty"`
	ID           string `json:"id"`
	ClaimID      string `json:"claim_id"`
	ReviewQuestion
}

func reviewQuestionPath(phase string) string { return "review_questions." + phase }

func reviewQuestions(vars map[string]any, phase string) ([]reviewQuestionWork, error) {
	value, ok := conditions.DotPathGet(vars, reviewQuestionPath(phase))
	if !ok {
		return nil, nil
	}
	raw, ok := value.(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("review question state is invalid")
	}
	var out []reviewQuestionWork
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode review questions: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("review questions contain trailing data")
	}
	return out, nil
}

func questionClaims(def workflowdef.ReviewLoopDef, verdict map[string]string) ([]VerdictClaim, error) {
	fields, err := ParseVerdictClaims(def, verdict)
	if err != nil {
		return nil, err
	}
	var out []VerdictClaim
	for _, field := range sortedClaimFields(fields) {
		out = append(out, fields[field]...)
	}
	return out, nil
}

// Registered questions keep their identity through investigation and closure.
func (m *RunManager) prepareReviewQuestions(ctx context.Context, run *api.WorkflowRun, def workflowdef.ReviewLoopDef, verdict map[string]string, vars map[string]any) (map[string]any, error) {
	if def.FollowupAttempts == 0 {
		return vars, nil
	}
	claims, err := questionClaims(def, verdict)
	if err != nil {
		return vars, err
	}
	known, err := reviewQuestions(vars, run.CurrentPhase)
	if err != nil {
		return vars, err
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return vars, err
	}
	facts, err := m.CoverageFacts(ctx, run, manifest)
	if err != nil {
		return vars, err
	}
	terminal := ReviewLoopVerdictTerminal(def, verdict)
	if terminal {
		rules, err := m.VerdictRulesFor(ctx, run)
		if err != nil {
			return vars, err
		}
		var missing, expected []string
		for id := range rules.KnownClaims {
			expected = append(expected, id)
			if !slices.ContainsFunc(claims, func(c VerdictClaim) bool { return c.ID == id }) {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			slices.Sort(missing)
			slices.Sort(expected)
			return vars, &tools.ToolReject{Code: ReviewLoopVerdictInvalidCode, Data: map[string]any{"action": "edit_submission", "missing_claim_ids": missing, "expected_claim_ids": expected, "reason": "missing_claim_outcomes"}}
		}
	}
	known, err = registerReviewQuestions(def, claims, known, facts, terminal)
	for i := range known {
		known[i].ReviewWorkID = known[i].ID + "/review"
	}
	if err != nil {
		return vars, err
	}
	if terminal {
		if m.WorkerTasks == nil {
			return vars, fmt.Errorf("review worker ledger unavailable")
		}
		tasks, err := m.WorkerTasks(ctx, run.ID)
		if err != nil {
			return vars, err
		}
		review, err := ParseVerdictCoverage(def, verdict)
		if err != nil {
			return vars, err
		}
		if err := checkQuestionClosure(def, claims, known, tasks, run.CurrentPhase, review); err != nil {
			return vars, err
		}
		return vars, nil
	}
	if !slices.ContainsFunc(claims, func(claim VerdictClaim) bool { return def.ClassOf(claim.Status) == workflowdef.ClaimOpen }) {
		return vars, rejectReviewQuestion("open_question_required", "")
	}
	if m.WorkerTasks == nil {
		return vars, fmt.Errorf("review worker ledger unavailable")
	}
	tasks, err := m.WorkerTasks(ctx, run.ID)
	if err != nil {
		return vars, err
	}
	if err := checkQuestionContinuation(def, claims, known, tasks, run.CurrentPhase); err != nil {
		return vars, err
	}
	raw, err := json.Marshal(known)
	if err != nil {
		return vars, err
	}
	return SetHostVar(vars, reviewQuestionPath(run.CurrentPhase), string(raw)), nil
}

func registerReviewQuestions(def workflowdef.ReviewLoopDef, claims []VerdictClaim, known []reviewQuestionWork, facts reviewcoverage.Facts, terminal bool) ([]reviewQuestionWork, error) {
	for _, claim := range claims {
		if def.ClassOf(claim.Status) != workflowdef.ClaimOpen {
			continue
		}
		i := slices.IndexFunc(known, func(q reviewQuestionWork) bool { return q.ClaimID == claim.ID })
		if i >= 0 {
			if claim.Question != nil && reviewcoverage.Identity(*claim.Question) != reviewcoverage.Identity(known[i].ReviewQuestion) {
				return nil, rejectReviewQuestion("scope_changed", known[i].ID)
			}
			continue
		}
		if terminal {
			return nil, rejectReviewQuestion("registration_required", "question/"+url.PathEscape(claim.ID))
		}
		if claim.Question == nil || strings.TrimSpace(claim.Question.MissingFact) == "" || len(claim.Question.Obligations) == 0 {
			return nil, rejectReviewQuestion("question_fields_required", "question/"+url.PathEscape(claim.ID))
		}
		for _, id := range claim.Question.Obligations {
			if !slices.ContainsFunc(facts.Obligations, func(f reviewcoverage.Fact) bool { return f.ID == id }) {
				return nil, rejectReviewQuestion("unknown_obligation", "question/"+url.PathEscape(claim.ID))
			}
		}
		known = append(known, reviewQuestionWork{ID: "question/" + url.PathEscape(claim.ID), ClaimID: claim.ID, ReviewQuestion: *claim.Question})
	}
	return known, nil
}
