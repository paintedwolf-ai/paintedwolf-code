package hitl

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
)

// Scope lifetimes for durable rungs — the ladder's only duration literals.
const (
	ProjectLeaseDuration = 7 * 24 * time.Hour
	DeviceLeaseDuration  = 30 * 24 * time.Hour
)

// ApprovalGrantID derives the stable identity of one reusable grant from the
// facts a lease binds to. Every mint site and Settings row shares it, so an
// offer, its installed grant, and the ledger name one authority.
func ApprovalGrantID(scope ApprovalGrantScope, category, pattern, chatSessionID, projectID string, witness ApprovalGrantWitness, exactActions []string) string {
	raw := strings.Join([]string{string(scope), category, pattern, chatSessionID, projectID,
		fmt.Sprintf("%t", witness.FSJailed), witness.Egress, witness.RootsDigest,
		strings.Join(exactActions, "\x1f")}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return "grant_" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// ExactActionSetOffer leases an enumerated canonical action set for the chat.
func ExactActionSetOffer(action ProposedAction, exactActions []string) ApprovalGrantOffer {
	return ExactActionSetOfferAtScope(action, exactActions, ApprovalGrantScopeChat)
}

// ExactActionOffer leases one canonical action and boundary for the chat.
func ExactActionOffer(action ProposedAction) ApprovalGrantOffer {
	return ExactActionOfferAtScope(action, ApprovalGrantScopeChat)
}

// ExactActionOfferAtScope leases one canonical action at chat or project scope.
func ExactActionOfferAtScope(action ProposedAction, scope ApprovalGrantScope) ApprovalGrantOffer {
	key := GrantKey(action)
	if key == "" {
		return ApprovalGrantOffer{}
	}
	offer := ExactActionSetOfferAtScope(action, []string{key}, scope)
	offer.Coverage = CoverageOnlyExactActionArgs
	offer.Grant.Coverage = offer.Coverage
	return offer
}

// ExactActionSetOfferAtScope mints an exact-action-set lease at chat or project
// scope. Any other scope is clamped to chat: an enumerated action set has no
// device-wide meaning.
func ExactActionSetOfferAtScope(action ProposedAction, exactActions []string, scope ApprovalGrantScope) ApprovalGrantOffer {
	if len(exactActions) == 0 {
		return ApprovalGrantOffer{}
	}
	for _, key := range exactActions {
		if strings.TrimSpace(key) == "" {
			return ApprovalGrantOffer{}
		}
	}
	if scope != ApprovalGrantScopeChat && scope != ApprovalGrantScopeProject {
		scope = ApprovalGrantScopeChat
	}
	witness := BoundaryWitness(action.Execution.Contained)
	digest := sha256.Sum256([]byte(strings.Join(exactActions, "\x00")))
	pattern := base64.RawURLEncoding.EncodeToString(digest[:])
	now := time.Now().UTC()
	grant := ApprovalGrant{
		Scope:           scope,
		Predicate:       ApprovalGrantPredicate{Category: ApprovalGrantCategoryActionSet, Pattern: pattern},
		ProjectID:       action.Scope.ProjectID,
		ProjectDir:      action.Scope.ProjectDir,
		Coverage:        CoverageOnlyEnumeratedActions,
		GrantedAt:       now,
		ReaskWhen:       "any action, argument, project, or confinement differs",
		ElevatedEffects: ElevatedEffectsForAction(action),
		Witness:         witness, ExactActionSet: append([]string(nil), exactActions...),
	}
	var rung ApprovalOptionRung
	switch scope {
	case ApprovalGrantScopeProject:
		rung = ApprovalRungProject
		expires := now.Add(ProjectLeaseDuration)
		grant.Title = TitleAllowForThisProject
		grant.ExpiresAt = &expires
		grant.ExpiresWhen = ExpiresIn7DaysOrRevoked
	default:
		rung = ApprovalRungChat
		grant.ChatSessionID = action.Scope.ChatSession()
		grant.Title = TitleAllowForThisChat
		grant.ExpiresWhen = ExpiresWhenChatDeleted
	}
	grant.ID = ApprovalGrantID(grant.Scope, ApprovalGrantCategoryActionSet, pattern, grant.ChatSessionID, grant.ProjectID, witness, exactActions)
	return ApprovalGrantOffer{
		ID: grant.ID, Rung: rung, Scope: grant.Scope, Title: grant.Title, Coverage: grant.Coverage,
		ExpiresWhen: grant.ExpiresWhen, ReaskWhen: grant.ReaskWhen, Subject: gate.ReuseExactAction, Grant: grant,
	}
}

// CommandNetworkOffer leases one command's mediated network for the chat. It
// is the chat slot on the second tunnel card a held command raises: the
// subject widens from one host to every host that exact command reaches, and
// every host is still observed and recorded. Never minted where a detection,
// credential exposure, or the high-risk band is on the card.
func CommandNetworkOffer(action ProposedAction) (ApprovalGrantOffer, bool) {
	command := strings.TrimSpace(action.Presentation.Command)
	if action.Invocation.Tool != "network" || command == "" || action.Scope.ChatSession() == "" {
		return ApprovalGrantOffer{}, false
	}
	witness := BoundaryWitness(action.Execution.Contained)
	grant := ApprovalGrant{
		Scope:         ApprovalGrantScopeChat,
		Predicate:     ApprovalGrantPredicate{Category: ApprovalGrantCategoryEgressCommand, Pattern: command},
		ChatSessionID: action.Scope.ChatSession(),
		ProjectID:     action.Scope.ProjectID,
		ProjectDir:    action.Scope.ProjectDir,
		Title:         TitleAllowCommandNetworkForThisChat,
		Coverage:      CoverageCommandNetwork(command),
		GrantedAt:     time.Now().UTC(),
		ExpiresWhen:   ExpiresWhenChatDeleted,
		ReaskWhen:     ReaskWhenDifferentCommand,
		Witness:       witness,
	}
	grant.ID = ApprovalGrantID(grant.Scope, ApprovalGrantCategoryEgressCommand, command, grant.ChatSessionID, grant.ProjectID, witness, nil)
	return ApprovalGrantOffer{
		ID: grant.ID, Rung: ApprovalRungChat, Scope: grant.Scope, Title: grant.Title, Coverage: grant.Coverage,
		ExpiresWhen: grant.ExpiresWhen, ReaskWhen: grant.ReaskWhen, Subject: gate.ReusePredicate, Grant: grant,
	}, true
}
