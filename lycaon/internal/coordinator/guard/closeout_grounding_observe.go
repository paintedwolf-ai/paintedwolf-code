package guard

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// ObserveCoordinatorCloseoutGrounding publishes closeout citation facts for OAR.
func ObserveCoordinatorCloseoutGrounding(
	ctx context.Context,
	ledger guidance.CloseoutEvidenceReader,
	sess *api.Session,
	history []api.Message,
	surfaceID string,
	report guidance.CoordinatorCompletionReport,
	turnTools []string,
	roots evidence.CitationRoots,
	gc *oar.GuardContext,
) (CloseoutGroundingVerdict, error) {
	verdict, err := EvaluateCoordinatorCloseoutGrounding(ctx, ledger, sess, history, surfaceID, report, turnTools, roots)
	if err != nil {
		return verdict, err
	}
	if gc == nil {
		return verdict, nil
	}
	gc.Session.Surface = strings.TrimSpace(surfaceID)
	gc.Grounding.CitationFieldsPresent = len(report.CitedEvidence) > 0 || len(report.CitedURLs) > 0
	if verdict.CitationsRequired {
		gc.Rejection.RejectObservation = "citations_required"
		putCloseoutRejectData(gc, verdict.HintData,
			guidance.SynthCitationsRequiredCode, guidance.InvestCitationsRequiredCode)
		return verdict, nil
	}
	publishCloseoutCitationFacts(gc, verdict)
	return verdict, nil
}

// publishCloseoutCitationFacts publishes citation observations.
func publishCloseoutCitationFacts(gc *oar.GuardContext, verdict CloseoutGroundingVerdict) {
	if gc == nil {
		return
	}
	offenders := append([]string(nil), verdict.Offenders...)
	switch {
	case len(verdict.UnobservedHandles) > 0:
		gc.Grounding.UnobservedCitedHandles = append([]string(nil), verdict.UnobservedHandles...)
		putCloseoutRejectData(gc, verdict.HintData,
			guidance.SynthHandleNotInLegsCode, guidance.InvestHandleNotObservedCode)
	case len(verdict.UnobservedURLs) > 0:
		gc.Grounding.UnobservedCitedURLs = append([]string(nil), verdict.UnobservedURLs...)
		putCloseoutRejectData(gc, verdict.HintData,
			guidance.SynthURLNotObservedCode, guidance.InvestURLNotObservedCode)
	case verdict.CitationUnverifiable:
		if len(offenders) == 0 {
			return
		}
		gc.Grounding.CitationUnverifiable = true
		putCloseoutRejectData(gc, verdict.HintData,
			guidance.SynthCitationUnverifiableCode, guidance.InvestCitationUnverifiableCode)
	}
}

func putCloseoutRejectData(gc *oar.GuardContext, hintData map[string]any, codes ...string) {
	for _, code := range codes {
		gc.PutRejectData(code, hintData)
	}
}
