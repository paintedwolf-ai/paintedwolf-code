package hitl

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
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

func validApprovalRung(rung ApprovalOptionRung) bool { return rungRank(rung) < len(approvalRungOrder) }

// rungRank is the ordering key within a group. An unknown rung ranks last;
// validApprovalRung rejects it before ordering matters.
func rungRank(rung ApprovalOptionRung) int {
	for i, candidate := range approvalRungOrder {
		if candidate == rung {
			return i
		}
	}
	return len(approvalRungOrder)
}

// approvalGroupOrder is the render order of ladder group headings. The primary
// ladder is the empty heading and leads.
var approvalGroupOrder = []string{
	"", GroupAlsoAllow, GroupHostResources, GroupQuiet, GroupRedaction, GroupTrust,
}

func validApprovalGroup(group string) bool { return groupOrder(group) < len(approvalGroupOrder) }

func groupOrder(group string) int {
	for i, candidate := range approvalGroupOrder {
		if candidate == group {
			return i
		}
	}
	return len(approvalGroupOrder)
}

func sortApprovalOptions(in []ApprovalOption) []ApprovalOption {
	out := append([]ApprovalOption(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := strings.TrimSpace(out[i].Group), strings.TrimSpace(out[j].Group)
		if gi != gj {
			if groupOrder(gi) != groupOrder(gj) {
				return groupOrder(gi) < groupOrder(gj)
			}
			return gi < gj
		}
		return rungRank(out[i].Rung) < rungRank(out[j].Rung)
	})
	return out
}

// faceRungs is the recommended-option order: chat first, wider rungs last.
var faceRungs = []ApprovalOptionRung{ApprovalRungChat, ApprovalRungOnce, ApprovalRungDay, ApprovalRungProject, ApprovalRungDevice}

// refaceAfterFilter recomputes the face when narrowing the options removed it.
func (p *ApprovalPlan) refaceAfterFilter(face FaceContext) error {
	if _, ok := p.Option(p.RecommendedOptionID); ok {
		return nil
	}
	id, err := computeRecommendedOptionID(p.Subject.Kind, p.Reasons, p.Options, face)
	if err != nil {
		return err
	}
	p.RecommendedOptionID = id
	return nil
}

// computeRecommendedOptionID selects the recommended option.
func computeRecommendedOptionID(kind ApprovalSubjectKind, reasons []api.ApprovalGate, options []ApprovalOption, face FaceContext) (string, error) {
	if kind == ApprovalSubjectSecret {
		return secretFace(options, face.SecretManaged)
	}
	if len(reasons) == 1 && reasons[0] == api.GateAuthorityMisuse {
		for _, option := range options {
			if option.Kind == ApprovalOptionQuiet && option.Rung == ApprovalRungChat && !option.Disabled {
				return option.ID, nil
			}
		}
	}
	primary := primaryLadder(options)
	if len(primary) == 0 {
		return "", fmt.Errorf("approval plan has no face among %d options", len(options))
	}
	for _, want := range faceRungs {
		for _, option := range primary {
			if option.Rung == want && !option.Disabled {
				return option.ID, nil
			}
		}
	}
	return "", fmt.Errorf("approval plan has no selectable face among %d options", len(options))
}

// secretFace prefers tracking for raw values and release for managed values.
func secretFace(options []ApprovalOption, managed bool) (string, error) {
	if !managed {
		for _, option := range options {
			if option.Kind == ApprovalOptionTracked && !option.Disabled {
				return option.ID, nil
			}
		}
	}
	for _, option := range options {
		if option.Kind == ApprovalOptionLease && option.Scope == ApprovalGrantScopeChat &&
			strings.TrimSpace(option.Group) == "" && !option.Disabled {
			return option.ID, nil
		}
	}
	for _, option := range options {
		if option.Kind == ApprovalOptionCurrentAction && option.Rung == ApprovalRungUnchanged &&
			!option.Disabled {
			return option.ID, nil
		}
	}
	return "", fmt.Errorf("secret approval plan has no selectable send")
}

// primaryLadder is the unnamed ladder, falling back to every non-quiet option
// when a card carries only grouped rungs.
func primaryLadder(options []ApprovalOption) []ApprovalOption {
	var primary []ApprovalOption
	for _, option := range options {
		if option.Kind == ApprovalOptionQuiet {
			continue
		}
		if strings.TrimSpace(option.Group) == "" {
			primary = append(primary, option)
		}
	}
	if len(primary) > 0 {
		return primary
	}
	for _, option := range options {
		if option.Kind != ApprovalOptionQuiet {
			primary = append(primary, option)
		}
	}
	return primary
}
