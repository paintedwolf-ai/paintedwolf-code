package hitl

import (
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

// SecretPermission is a prepared, value-free contribution to a capability card.
type SecretPermission struct {
	ConnectPorts []uint16
	Screen       SecretScreen
	Offers       []ApprovalGrantOffer
	Fingerprints []secretmatch.SecretFingerprint
}

// Key binds key to the secret fingerprints and recipients this permission covers.
func (permission *SecretPermission) Key(key string) string {
	if strings.TrimSpace(key) == "" {
		return ""
	}
	if permission == nil {
		return key
	}
	return key + ":" + secretmatch.FingerprintDigest(permission.Fingerprints) + ":" + secretmatch.RecipientDigest(permission.Screen.Recipients)
}

// ComposeSecretPermission makes every enabled duration carry both permissions.
// The once option releases only the held invocation; quiet cannot hide a disclosure.
func ComposeSecretPermission(plan *ApprovalPlan, action ProposedAction, permission *SecretPermission, face FaceContext) (*ApprovalPlan, error) {
	if permission == nil {
		return plan, nil
	}
	if err := permission.validate(action); err != nil {
		return nil, err
	}
	offers := map[ApprovalOptionRung]ApprovalGrantOffer{}
	for _, offer := range permission.Offers {
		offers[offer.Rung] = offer
	}
	options := append([]ApprovalOption(nil), plan.Options...)
	var additionalPorts []uint16
	for i := range options {
		option := &options[i]
		if option.Disabled {
			continue
		}
		if option.Kind == ApprovalOptionCurrentAction {
			if !slices.ContainsFunc(option.Authority, func(delta ApprovalAuthorityDelta) bool { return delta.Kind == AuthorityCurrentAction }) {
				option.Authority = append(append([]ApprovalAuthorityDelta(nil), option.Authority...), ApprovalAuthorityDelta{Kind: AuthorityCurrentAction})
			}
			option.Coverage += "; protected values used only in this invocation"
			continue
		}
		offer, ok := offers[option.Rung]
		if !ok || option.Kind == ApprovalOptionQuiet || option.Group != "" {
			option.Disabled = true
			option.Note = "Choose a duration that covers both access and protected values."
			continue
		}
		grant := offer.Grant
		// A compound day permission cannot outlive its chat-scoped capability.
		if option.Scope == ApprovalGrantScopeChat && grant.Scope != ApprovalGrantScopeChat {
			grant.Scope = ApprovalGrantScopeChat
			grant.ChatSessionID = action.Scope.ChatSession()
			grant.ID += "-chat-" + action.Scope.ChatSession()
		}
		delta := ApprovalAuthorityDelta{Kind: AuthorityGenericGrant, Grant: &grant}
		option.Authority = append(append([]ApprovalAuthorityDelta(nil), option.Authority...), delta)
		if len(permission.ConnectPorts) > 0 {
			if option.Rung != ApprovalRungDay && option.Rung != ApprovalRungChat {
				option.Disabled = true
				option.Note = "Local service connections last only for this chat."
			} else {
				missing := uncoveredServicePorts(permission.ConnectPorts, option.Authority, action.Scope.ChatSession())
				if len(missing) > 0 {
					connection := *permission
					connection.ConnectPorts = missing
					option.Authority = append(option.Authority, secretServiceConnection(action, &connection, *option))
					option.Coverage += "; connections to the named local service ports for this chat"
					additionalPorts = append(additionalPorts, missing...)
				}
			}
		}
		option.Coverage += "; " + offer.Coverage
		option.ReaskWhen += "; a secret value or recipient changes"
	}
	subject := plan.Subject
	subject.Kind = ApprovalSubjectActionSet
	subject.Targets = append(append([]ApprovalTarget(nil), subject.Targets...), ApprovalTarget{
		Kind: "secret", Label: strings.Join(permission.Screen.SecretNames, ", "),
		Details: map[string]any{secretGenericShapeDetail: "Protected values"},
	})
	if subject.Targets[len(subject.Targets)-1].Label == "" {
		subject.Targets[len(subject.Targets)-1].Label = "Protected values"
	}
	if len(additionalPorts) > 0 {
		slices.Sort(additionalPorts)
		subject.Targets = append(subject.Targets, secretServiceConnectionTarget(slices.Compact(additionalPorts)))
	}
	presentation := plan.Presentation
	presentation.Impact += " " + SecretImpact(&permission.Screen)
	presentation.Location = compileSecretLocation(&permission.Screen)
	presentation.ConsequenceBand = string(api.ConsequenceBandHighRisk)
	presentation.ConsequenceCode = string(api.ConsequenceCodeSecret)
	presentation.Cited = append(append([]PresentedFact(nil), presentation.Cited...), PresentedFact{
		Gate: api.GateSecretOutbound, Key: "secret.use", Value: "Managed values and their recipients", Source: "managed_secret",
	})
	reasons := append(append([]api.ApprovalGate(nil), plan.Reasons...), api.GateSecretOutbound)
	composed, err := NewApprovalPlan(action, plan.Stage, subject, presentation, reasons, options, face)
	if err != nil {
		return nil, err
	}
	return composed.WithHeld(plan.Held.merged(permission.Screen.Held))
}
