package grounding

import (
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
)

const (
	AmbientUngroundedCompletionCode = "AMBIENT_UNGROUNDED_COMPLETION"
	AmbientGroundingEscalatedCode   = "AMBIENT_GROUNDING_ESCALATED"
	CoordinatorUngroundedClaimCode  = "COORDINATOR_UNGROUNDED_CLAIM"
	CoordinatorCriteriaUnmetCode    = "COORDINATOR_CRITERIA_UNMET"
	CoordinatorEscalatedCode        = "COORDINATOR_GROUNDING_ESCALATED"
)

// ObserveVerdict records a typed grounding verdict on the OAR guard context.
func ObserveVerdict(gc *oar.GuardContext, verdict GroundingVerdict) {
	if gc == nil || verdict.OK || verdict.Code == "" {
		return
	}
	code := strings.TrimSpace(verdict.Code)
	data := map[string]any{"grounding_verdict": code, "grounding_reason": verdict.Reason}
	if verdict.LegID != "" {
		data["leg_id"] = verdict.LegID
	}
	switch code {
	case CoordinatorEscalatedCode:
		gc.Grounding.GroundingEscalated = true
		gc.Grounding.ClaimsCompletion = true
	case AmbientGroundingEscalatedCode:
		gc.Grounding.GroundingEscalated = true
		gc.Grounding.ClaimsCompletion = false
	case CoordinatorUngroundedClaimCode:
		gc.Grounding.ClaimsCompletion = true
		gc.Grounding.HasMatchingLedgerJob = false
	case CoordinatorCriteriaUnmetCode:
		gc.Grounding.ClaimsCompletion = true
		gc.Grounding.HasMatchingLedgerJob = true
	case AmbientUngroundedCompletionCode:
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
