package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
)

// PublishCloseoutGrounding publishes a verdict's facts for OAR.
func PublishCloseoutGrounding(gc *oar.GuardContext, surfaceID string, draft guidance.CloseoutDraft, verdict CloseoutGroundingVerdict) {
	if gc == nil {
		return
	}
	gc.Session.Surface = strings.TrimSpace(surfaceID)
	if verdict.CitationsRequired {
		gc.Rejection.RejectObservation = "citations_required"
		putCloseoutRejectData(gc, verdict.HintData,
			guidance.SynthCitationsRequiredCode, guidance.InvestCitationsRequiredCode)
		return
	}
	publishCloseoutCitationFacts(gc, verdict)
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
