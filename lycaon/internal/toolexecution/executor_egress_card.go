package toolexecution

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
)

// egressRaise is everything one endpoint needs to raise its card.
type egressRaise struct {
	cmd       confine.EgressCommand
	ep        egressproxy.Endpoint
	action    hitl.ProposedAction
	subject   egressAskSubject
	decision  *gate.Decision
	facts     gate.Facts
	detection *hitl.DetectionMatch
	matches   []hitl.ApprovalRuleMatch
}

// settleEgressDecision records the outcome of one endpoint's card.
func (e *Network) settleEgressDecision(ctx context.Context, raise egressRaise, allowed bool, final *hitl.CheckpointResponse) {
	if allowed {
		e.recordHostVisit(ctx, raise.cmd, raise.action.ChatSession(), raise.ep.Host)
		return
	}
	e.Rejections.rememberCapabilityDenial(raise.cmd.SessionID, raise.cmd.ToolCallID, raise.cmd.EffectActionID(), final)
}

// Posture and the reviewed boundary determine widening offers.
func egressWidening(raise egressRaise) (face bool, menu bool) {
	if raise.detection != nil || raise.facts.SecretExposed || raise.subject.Declared != nil {
		return false, false
	}
	posture := raise.decision.Posture
	if posture.Widens() {
		return true, false
	}
	return false, posture.Ladder().Widening == gate.WidenMenuOnly
}

// Posture determines whether command-network authority is a primary choice or
// a menu choice. As a primary choice it sits beside the host's chat permission,
// never in its place, so a posture that widens never asks more than one that
// does not.
func (e *Network) egressOffers(raise egressRaise) []hitl.ApprovalGrantOffer {
	offers := e.Approvals.grantOffers(raise.action, &hitl.ApprovalResult{Decision: raise.decision})
	command, ok := hitl.CommandNetworkOffer(raise.action)
	if !ok {
		return offers
	}
	face, menu := egressWidening(raise)
	switch {
	case face:
		return besideChatSlot(offers, command)
	case menu:
		command.Group = hitl.GroupAlsoAllow
		return append(offers, command)
	}
	return offers
}

// besideChatSlot places wider right after the primary chat rung.
func besideChatSlot(offers []hitl.ApprovalGrantOffer, wider hitl.ApprovalGrantOffer) []hitl.ApprovalGrantOffer {
	out := make([]hitl.ApprovalGrantOffer, 0, len(offers)+1)
	placed := false
	for _, offer := range offers {
		out = append(out, offer)
		if !placed && offer.Rung == hitl.ApprovalRungChat && strings.TrimSpace(offer.Group) == "" {
			out = append(out, wider)
			placed = true
		}
	}
	if !placed {
		out = append(out, wider)
	}
	return out
}

// raiseEgressCard raises the single-endpoint card.
func (e *Network) raiseEgressCard(ctx context.Context, raise egressRaise) *hitl.CheckpointResponse {
	offers := e.egressOffers(raise)
	final, err := e.Approvals.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
		Action:                 raise.action,
		Title:                  raise.subject.Title,
		ToolCallID:             raise.cmd.ToolCallID,
		ProjectID:              raise.cmd.ProjectID,
		ApprovalMatches:        raise.matches,
		Decision:               raise.decision,
		Explanation:            raise.subject.Explanation,
		Detection:              raise.detection,
		DeclaredEndpoints:      raise.subject.Declared,
		GrantOffers:            offers,
		SkipGrantOfferAutofill: true,
		// The ledger records the attempted endpoint, even when the card covers a declared set.
		DetectionEndpoints: []authzledger.CapabilityEndpoint{{
			Host: raise.ep.Host, Port: raise.ep.Port, Transport: string(raise.ep.Transport), Allowed: true, Attempts: 1,
		}},
		CoalesceKey: raise.subject.ApprovalKey,
	})
	if err != nil {
		return nil
	}
	return final
}
