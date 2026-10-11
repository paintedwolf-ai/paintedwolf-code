package review

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

// publishVerdictEvidence is an idempotent outbox delivery of an accepted receipt.
func (m *Verdicts) publishVerdictEvidence(ctx context.Context, op runstate.VerdictOperation, outcome runstate.ReviewOutcome) error {
	if op.EvidencePublished {
		return nil
	}
	if outcome.Valid {
		var input runstate.VerdictSubmission
		if err := json.Unmarshal([]byte(op.EvidenceJSON), &input); err != nil {
			return err
		}
		run, err := m.Runs.Get(ctx, op.RunID)
		if err != nil {
			return err
		}
		manifest, err := m.Resolver.ForRun(ctx, run)
		if err != nil {
			return err
		}
		phase, ok := manifest.PhaseByID(op.Phase)
		if !ok || phase.ReviewLoop == nil {
			return fmt.Errorf("committed review phase unavailable: %s", op.Phase)
		}
		cited := allVerdictCitations(*phase.ReviewLoop, input.Verdict, input.Cited)
		if outcome.Grounding != nil && len(outcome.Grounding.CitedEvidence) > 0 {
			cited = outcome.Grounding.CitedEvidence
		}
		if err := m.persistReviewLoopEvidence(ctx, input.SessionID, run, *phase.ReviewLoop, op.Phase, input.Verdict, cited, input.CitedURLs, op.EvidenceRecordID, op.CreatedAt, outcome.Attempt); err != nil {
			return err
		}
	}
	return m.Records.MarkVerdictEvidencePublished(ctx, op.ToolCallID)
}
