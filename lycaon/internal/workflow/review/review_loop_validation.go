package review

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

type reviewValidation struct {
	Outcome runstate.ReviewOutcome
	Vars    map[string]any
}

func (m *Verdicts) validateReviewSubmission(ctx context.Context, active *api.WorkflowRun, rl workflowdef.ReviewLoopDef, verdict map[string]string, vars map[string]any, citedEvidence []api.CitationGroundingCitedEvidence, citedURLs []string) (reviewValidation, error) {
	out := runstate.ReviewOutcome{
		Applied:     true,
		Phase:       active.CurrentPhase,
		EvidenceKey: rl.EvidenceKey,
	}
	rules, err := m.VerdictRulesFor(ctx, active)
	if err != nil {
		return reviewValidation{}, err
	}
	shapeValid := workflowvalidation.ValidateReviewLoopVerdict(rl, verdict, rules) == nil
	out.Valid = shapeValid
	questionVars := vars
	if out.Valid && rl.FollowupAttempts > 0 {
		questionVars, err = m.Questions.Prepare(ctx, active, rl, verdict, vars)
		if err != nil {
			out.Valid = false
			if rejection := tools.AsToolReject(err); rejection != nil {
				out.QuestionIssue = rejection
			} else {
				return reviewValidation{}, err
			}
		}
	}
	if shapeValid && workflowvalidation.ReviewLoopVerdictTerminal(rl, verdict) {
		out.CoverageIssue, err = m.Coverage.ValidateReview(ctx, active, rl, verdict)
		if err != nil {
			return reviewValidation{}, err
		}
		if out.CoverageIssue != nil {
			out.Valid = false
		}
		out.InventoryIssue, err = m.Coverage.checkReviewInventory(ctx, active, rl, verdict)
		if err != nil {
			return reviewValidation{}, err
		}
		if out.InventoryIssue != nil {
			out.Valid = false
		}
		owed, captured := runstate.EffectiveReviewAgents(active.CurrentPhase, rl, vars)
		if !captured {
			return reviewValidation{}, fmt.Errorf("reviewer roster unavailable for phase %q", active.CurrentPhase)
		}
		if missing := m.missingReviewAgents(ctx, active, owed); len(missing) > 0 {
			out.Valid = false
			out.MissingAgents = missing
		} else if m.VerdictGrounding != nil {
			eval, evalErr := m.VerdictGrounding(ctx, active.SessionID, allVerdictCitations(rl, verdict, citedEvidence), citedURLs, owed)
			if evalErr != nil {
				return reviewValidation{}, evalErr
			}
			if eval.Code != "" {
				out.Valid = false
				out.GroundingCode = eval.Code
				out.UngroundedCount = eval.UngroundedCount
				out.UngroundedSample = eval.UngroundedSample
				out.UncitedReviewers = eval.UncitedReviewers
				out.ObservedHandles = eval.ObservedHandles
			} else {
				out.Grounding = eval.Grounding
			}
		}
	}
	return reviewValidation{Outcome: out, Vars: questionVars}, nil
}
