package gate

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Posture selects the active gates and default approval scope.
type Posture string

const (
	// PostureLight retains credential, authority, consent, and boundary reviews.
	PostureLight Posture = "light"
	// PostureBalanced also reviews sensitive paths, outside roots, and agent-chosen destinations.
	PostureBalanced Posture = "balanced"
	// PostureStrict adds sending content out after a credential read, the first
	// reach to a new host, and unleased MCP.
	PostureStrict Posture = "strict"
)

// DefaultPosture is the out-of-the-box ask-line.
const DefaultPosture = PostureBalanced

// PostureFromString uses the default for empty or unknown tokens.
func PostureFromString(s string) Posture {
	switch Posture(strings.TrimSpace(strings.ToLower(s))) {
	case PostureLight:
		return PostureLight
	case PostureStrict:
		return PostureStrict
	default:
		return PostureBalanced
	}
}

// ValidPosture reports whether s is a recognized posture token (light|balanced|strict).
func ValidPosture(s string) bool {
	switch Posture(strings.TrimSpace(strings.ToLower(s))) {
	case PostureLight, PostureBalanced, PostureStrict:
		return true
	}
	return false
}

// Strictness orders postures by their active gate sets.
func (p Posture) Strictness() int {
	switch p {
	case PostureLight:
		return 0
	case PostureStrict:
		return 2
	case PostureBalanced:
		return 1
	default:
		return DefaultPosture.Strictness()
	}
}

// Stricter selects the stricter configured posture; empty means unset.
func Stricter(a, b Posture) Posture {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case b.Strictness() > a.Strictness():
		return b
	default:
		return a
	}
}

// postureReleasesChatSecrets defines postures releasing chat-generated secrets locally without prompting.
var postureReleasesChatSecrets = map[Posture]bool{
	PostureLight:    true,
	PostureBalanced: true,
}

// ReleasesChatSecretLocally reports whether the secret gate stays silent for hit.
func (p Posture) ReleasesChatSecretLocally(hit *SecretHit) bool {
	return hit != nil && hit.ChatGenerated && hit.RecipientsLocal && postureReleasesChatSecrets[normalize(p)]
}

// postureQuietsPublicRegistries defines postures where a tunnel to a public
// package registry is not an agent-chosen destination.
var postureQuietsPublicRegistries = map[Posture]bool{
	PostureLight:    true,
	PostureBalanced: true,
}

// QuietsPublicRegistry reports whether the destination is a public package
// registry this posture treats as baseline. Credential-class content in the chat
// withdraws the baseline, because the tunnel's payload is not screened.
func (p Posture) QuietsPublicRegistry(f Facts) bool {
	if f.Destination == nil || f.Destination.PublicRegistry == "" || !postureQuietsPublicRegistries[normalize(p)] {
		return false
	}
	return f.Ran.Has(ProducerExposure) && !f.SecretExposed
}

// AsksOnFirstHost reports whether the first reach to a host asks; it also sets
// the default egress posture.
func (p Posture) AsksOnFirstHost() bool { return p.Enables(api.GateFirstHost) }

// Widening decides whether a widened command-network grant is the card's chat
// option or only a menu choice.
type Widening string

const (
	// WidenFromFirstCard offers the widened grant as the chat option on the first card.
	WidenFromFirstCard Widening = "first_card"
	// WidenMenuOnly offers the widened grant only as an also-allow choice.
	WidenMenuOnly Widening = "menu_only"
)

// LadderPolicy is the card-ladder shaping a posture selects.
type LadderPolicy struct {
	Widening Widening
}

var postureLadder = map[Posture]LadderPolicy{
	PostureLight:    {Widening: WidenFromFirstCard},
	PostureBalanced: {Widening: WidenMenuOnly},
	PostureStrict:   {Widening: WidenMenuOnly},
}

// Ladder returns this posture's card-ladder policy.
func (p Posture) Ladder() LadderPolicy { return postureLadder[normalize(p)] }

// Widens reports whether the first card offers the widened grant as its chat option.
func (p Posture) Widens() bool { return p.Ladder().Widening == WidenFromFirstCard }

// Each posture includes the gates enabled by less restrictive postures.
var postureGates = map[Posture][]api.ApprovalGate{
	PostureLight: {
		api.GateSecretOutbound,
		api.GateConsentDrift,
		api.GateRemotePackageExecution,
		// Detection severity is filtered by the producer for this posture.
		api.GateAuthorityMisuse,
		api.GateOutsideRootsWrite,
		api.GateOutsideRootsRead,
		api.GateUnobservedChannel,
		api.GateUserRule,
		api.GateExplicitApprovalRequest,
	},
	PostureBalanced: {
		api.GateAgentPolicyChange,
		api.GateSecretOutbound,
		api.GateConsentDrift,
		api.GateRemotePackageExecution,
		api.GateRemotePackageExecutionKnown,
		api.GateAuthorityMisuse,
		api.GateSensitiveLocation,
		api.GateAgentChosenOutbound,
		api.GateOutsideRootsWrite,
		api.GateOutsideRootsRead,
		api.GateUnobservedChannel,
		api.GateUserRule,
		api.GateExplicitApprovalRequest,
		api.GateCapabilityWidening,
	},
	PostureStrict: {
		api.GateAgentPolicyChange,
		api.GateSecretOutbound,
		api.GateConsentDrift,
		api.GateRemotePackageExecution,
		api.GateRemotePackageExecutionKnown,
		api.GateAuthorityMisuse,
		api.GateSensitiveLocation,
		api.GateAgentChosenOutbound,
		api.GateSecretExposedOutbound,
		api.GateOutsideRootsWrite,
		api.GateOutsideRootsRead,
		api.GateUnobservedChannel,
		api.GateUserRule,
		api.GateExplicitApprovalRequest,
		api.GateCapabilityWidening,
		api.GateFirstHost,
		api.GateMCPUnleased,
	},
}

// Enables reports whether this posture runs the given gate.
func (p Posture) Enables(g api.ApprovalGate) bool {
	for _, candidate := range postureGates[normalize(p)] {
		if candidate == g {
			return true
		}
	}
	return false
}

// Gates returns the live set for this posture in citation-priority order.
func (p Posture) Gates() []api.ApprovalGate {
	all := api.AllApprovalGateValues()
	out := make([]api.ApprovalGate, 0, len(all))
	for _, candidate := range all {
		if p.Enables(candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

// Postures returns every posture, quietest first.
func Postures() []Posture {
	return []Posture{PostureLight, PostureBalanced, PostureStrict}
}

func normalize(p Posture) Posture {
	if _, ok := postureGates[p]; ok {
		return p
	}
	return DefaultPosture
}
