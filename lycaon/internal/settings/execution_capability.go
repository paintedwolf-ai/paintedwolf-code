package settings

import (
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

func (g *RuleApprovalGate) executionCapabilityCovers(action hitl.ProposedAction) bool {
	capability := hitl.ExecutionCapabilityForAction(action)
	if capability == "" || g.grants == nil {
		return false
	}
	for _, grant := range g.grants.live(action.Scope.ChatSession()) {
		if grant.Predicate.Category != hitl.ApprovalGrantCategoryExecutionCapability || grant.Predicate.Pattern != capability {
			continue
		}
		if grant.Scope != hitl.ApprovalGrantScopeChat || grant.ProjectID != action.Scope.ProjectID || grant.ChatSessionID != action.Scope.ChatSession() {
			continue
		}
		if grant.ExpiresAt != nil && !grant.ExpiresAt.After(time.Now()) {
			continue
		}
		return true
	}
	return false
}

func executionCapabilityDecision(result *hitl.ApprovalResult) bool {
	if result == nil || result.Decision == nil {
		return false
	}
	for _, fact := range result.Decision.Cited {
		if fact.Key == "boundary.execution" {
			return true
		}
	}
	return false
}

func (g *RuleApprovalGate) executionCapabilityOffers(action hitl.ProposedAction, result *hitl.ApprovalResult, ownsCard bool, group string) []hitl.ApprovalGrantOffer {
	capability := hitl.ExecutionCapabilityForAction(action)
	reuse := gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeChat, DayCarrier: gate.ScopeChat}
	offers := g.grantOffersForPredicate(action, ApprovalRule{Category: ApprovalCategory(hitl.ApprovalGrantCategoryExecutionCapability), Pattern: capability}, ownsCard, reuse, group)
	for i := range offers {
		offer := &offers[i]

		// Independent gates require a separate exact-action grant.
		if len(result.Decision.Gates()) > 1 || result.Decision.Primary != api.GateUnobservedChannel {
			exact := hitl.ExactActionOfferAtScope(action, hitl.ApprovalGrantScopeChat)
			if offer.TTLSeconds > 0 {
				exact = hitl.DayRung(exact)
			}
			offer.Authority = []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &offer.Grant, TTLSeconds: offer.TTLSeconds}, {Kind: hitl.AuthorityGenericGrant, Grant: &exact.Grant, TTLSeconds: exact.TTLSeconds}}
		}
	}
	return offers
}

func validateExecutionCapabilityGrant(grant hitl.ApprovalGrant) error {
	if grant.Scope != hitl.ApprovalGrantScopeChat || grant.ChatSessionID == "" || hitl.ExecutionCapabilityCoverage(grant.Predicate.Pattern) == "" {
		return fmt.Errorf("execution capability requires a recognized chat-scoped permission")
	}
	return nil
}
