package delegation

import (
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
)

// ObserveDelegationGroundingVerdict publishes the typed grounding verdict at its host boundary.
func ObserveDelegationGroundingVerdict(gc *oar.GuardContext, verdict GroundingVerdict) {
	if gc == nil || verdict.OK || verdict.Code == "" {
		return
	}
	code := strings.TrimSpace(verdict.Code)
	data := map[string]any{"grounding_verdict": code, "grounding_reason": verdict.Reason}
	if verdict.LegID != "" {
		data["leg_id"] = verdict.LegID
	}
	switch code {
	case "COORDINATOR_GROUNDING_ESCALATED":
		gc.Grounding.GroundingEscalated = true
		gc.Grounding.ClaimsCompletion = true
	case ambientGroundingEscalatedCode:
		gc.Grounding.GroundingEscalated = true
		gc.Grounding.ClaimsCompletion = false
	case "COORDINATOR_UNGROUNDED_CLAIM":
		gc.Grounding.ClaimsCompletion = true
		gc.Grounding.HasMatchingLedgerJob = false
	case "COORDINATOR_CRITERIA_UNMET":
		gc.Grounding.ClaimsCompletion = true
		gc.Grounding.HasMatchingLedgerJob = true
		gc.Grounding.LedgerCriteriaMet = false
	case ambientUngroundedCompletionCode:
		gc.Grounding.LastAuditUngrounded = true
		gc.Grounding.HasMatchingLedgerJob = false
		gc.Grounding.ClaimsCompletion = false
	default:
		// An ambient audit records failure without identifying offending citations.
		gc.Grounding.LastAuditUngrounded = true
		gc.Grounding.ClaimsCompletion = false
		annotateAmbientAuditSurface(gc, code)
	}
	gc.PutRejectData(code, data)
}

func annotateAmbientAuditSurface(gc *oar.GuardContext, code string) {
	switch code {
	case guidance.InvestHandleNotObservedCode, guidance.InvestURLNotObservedCode,
		guidance.InvestCitationUnverifiableCode, guidance.InvestCitationsRequiredCode:
		gc.Session.Surface = "implement_investigate"
	case guidance.SynthHandleNotInLegsCode, guidance.SynthURLNotObservedCode,
		guidance.SynthCitationUnverifiableCode, guidance.SynthCitationsRequiredCode:
		if gc.Session.Surface == "" {
			gc.Session.Surface = "implement_dispatch"
		}
	}
}
