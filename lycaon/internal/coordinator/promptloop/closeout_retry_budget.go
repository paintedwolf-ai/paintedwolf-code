package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/limits"
)

// closeoutRetryBudget is what one closeout refusal may spend. A citation
// refusal repairs references and spends the session tree's shared grounding
// friction as well as its own retry limit. A report-document refusal adds the
// conclusions the document owes, so it has its own small limit and leaves the
// friction budget alone.
type closeoutRetryBudget struct {
	kick    anchor.ID
	attempt int
	limit   int
	// friction is set for a citation refusal: the shared budget left after it.
	friction *guidance.GroundingFriction
	// document is the draft's current document fields, the fence a repair returns.
	document string
	// runReport marks a draft that delivers its run's report, which ends the
	// run as not accepted when document repair runs out.
	runReport bool
}

func (l *turnCloseout) closeoutRetryBudget(ctx context.Context, sessionID string, st *promptLoopTurnState, code, draftedContent string) closeoutRetryBudget {
	binding := completionReportBinding(st)
	if guidance.ReportDocumentObservation(code) != "" {
		st.closeoutRetry.documentAttempt++
		limit := limits.DefaultReportDocumentRetries
		if binding.CloseoutRetries > 0 {
			limit = binding.CloseoutRetries
		}
		return closeoutRetryBudget{
			kick:      anchor.CoordinatorReportDocument,
			attempt:   st.closeoutRetry.documentAttempt,
			limit:     limit,
			document:  guidance.ReportDocumentFence(draftedContent),
			runReport: binding.PhaseDeliversRunReport,
		}
	}
	st.closeoutRetry.attempt++
	limit := l.maxCitationGroundingRetries()
	if binding.CloseoutRetries > 0 {
		limit = binding.CloseoutRetries
	}
	budget := closeoutRetryBudget{kick: anchor.CoordinatorCitationGrounding, attempt: st.closeoutRetry.attempt, limit: limit}
	if l.Deps.RecordGroundingFriction != nil {
		friction := l.Deps.RecordGroundingFriction(ctx, sessionID)
		budget.friction = &friction
	}
	return budget
}

// lastAttempt is the last refusal that still earns a retry.
func (b closeoutRetryBudget) lastAttempt() int {
	last := b.limit
	if b.friction != nil {
		last = min(last, b.attempt+b.friction.Remaining-1)
	}
	return last
}

func (b closeoutRetryBudget) exhausted() bool {
	return b.attempt > b.lastAttempt()
}

// displayMax is the attempt count the repair kick states: the budget that
// actually applies, never less than the attempt it reports.
func (b closeoutRetryBudget) displayMax() int {
	return max(b.lastAttempt(), b.attempt)
}

// hintData adds what the repair kick renders beyond the refusal's own facts.
func (b closeoutRetryBudget) hintData(data map[string]any, unread ...[]jsonshape.Issue) map[string]any {
	if b.document == "" {
		return data
	}
	out := make(map[string]any, len(data)+4)
	for key, value := range data {
		out[key] = value
	}
	out["retained_document"] = b.document
	out["run_report"] = b.runReport
	out["allowed_top_level_keys"] = guidance.AllowedReportFenceKeys()
	if len(unread) > 0 && len(unread[0]) > 0 {
		out["offending_keys"] = guidance.ExtractOffendingKeys(unread[0])
	}
	return out
}
