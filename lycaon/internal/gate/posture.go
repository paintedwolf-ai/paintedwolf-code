package gate

import (
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

// Posture selects the active gates and default approval scope. The zero value
// means unset; any other value comes from ParsePosture or decoding.
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

// ParsePosture accepts exactly light, balanced, or strict; anything else is an
// error.
func ParsePosture(token string) (Posture, error) {
	p := Posture(token)
	if _, ok := postureRules[p]; !ok {
		return "", fmt.Errorf("unknown approval posture %q (use light, balanced, or strict)", token)
	}
	return p, nil
}

// UnmarshalText applies ParsePosture to YAML and JSON input. An empty value
// decodes as unset.
func (p *Posture) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*p = ""
		return nil
	}
	parsed, err := ParsePosture(string(text))
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}

// postureRule is everything a posture decides besides its gate roster.
type postureRule struct {
	strictness int
	ladder     LadderPolicy
	// releasesChatSecrets lets a secret generated for this chat reach the
	// chat's own local recipients without a card.
	releasesChatSecrets bool
	// quietsPublicRegistries treats a tunnel to a public package registry as
	// baseline rather than an agent-chosen destination.
	quietsPublicRegistries bool
	// quietsOwnedLocalServices lets a listener on an unprivileged port, and a
	// loopback connection to ports the chat owns, widen without a card.
	quietsOwnedLocalServices bool
	// reviewsConfinedWrites asks for a write even inside the chat's scratch
	// authority.
	reviewsConfinedWrites bool
	// leasesAgentPolicyFiles narrows an agent-policy lease to the exact files
	// instead of the trust surfaces the change touched.
	leasesAgentPolicyFiles bool
}

var postureRules = map[Posture]postureRule{
	PostureLight: {
		strictness: 0, ladder: LadderPolicy{Widening: WidenFromFirstCard},
		releasesChatSecrets: true, quietsPublicRegistries: true, quietsOwnedLocalServices: true,
	},
	PostureBalanced: {
		strictness: 1, ladder: LadderPolicy{Widening: WidenMenuOnly},
		releasesChatSecrets: true, quietsPublicRegistries: true, quietsOwnedLocalServices: true,
	},
	PostureStrict: {
		strictness: 2, ladder: LadderPolicy{Widening: WidenMenuOnly},
		reviewsConfinedWrites: true, leasesAgentPolicyFiles: true,
	},
}

// known returns the posture whose rule applies. A value that bypassed parsing
// reads as Strict, so it can only add asks.
func (p Posture) known() Posture {
	if _, ok := postureRules[p]; ok {
		return p
	}
	return PostureStrict
}

func (p Posture) rule() postureRule { return postureRules[p.known()] }

// Strictness orders postures by their active gate sets.
func (p Posture) Strictness() int { return p.rule().strictness }

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

// ReleasesChatSecretLocally reports whether the secret gate stays silent for hit.
func (p Posture) ReleasesChatSecretLocally(hit *SecretHit) bool {
	return hit != nil && !hit.Held && hit.ChatGenerated && hit.RecipientsLocal && p.rule().releasesChatSecrets
}

// QuietsPublicRegistry reports whether the destination is a public package
// registry this posture treats as baseline. Credential-class content in the chat
// withdraws the baseline, because the tunnel's payload is not screened.
func (p Posture) QuietsPublicRegistry(f Facts) bool {
	if f.Destination == nil || f.Destination.PublicRegistry == "" || !p.rule().quietsPublicRegistries {
		return false
	}
	return f.Ran.Has(ProducerExposure) && !f.SecretExposed
}

// ReviewsConfinedWrites reports whether a write inside scratch authority still asks.
func (p Posture) ReviewsConfinedWrites() bool { return p.rule().reviewsConfinedWrites }

// LeasesAgentPolicyFiles reports whether an agent-policy lease covers only the
// exact files a change touched.
func (p Posture) LeasesAgentPolicyFiles() bool { return p.rule().leasesAgentPolicyFiles }

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

// Ladder returns this posture's card-ladder policy.
func (p Posture) Ladder() LadderPolicy { return p.rule().ladder }

// Widens reports whether the first card offers the widened grant as its chat option.
func (p Posture) Widens() bool { return p.Ladder().Widening == WidenFromFirstCard }

// gateFloor is the quietest posture that runs each gate. Every stricter posture
// runs it too, so each roster contains the quieter ones by construction.
// GateIncompleteFacts has no floor: missing facts ask at every posture.
var gateFloor = map[api.ApprovalGate]Posture{
	api.GateSecretOutbound:         PostureLight,
	api.GateConsentDrift:           PostureLight,
	api.GateRemotePackageExecution: PostureLight,
	// Detection severity is filtered by the producer for each posture.
	api.GateAuthorityMisuse:         PostureLight,
	api.GateOutsideRootsWrite:       PostureLight,
	api.GateOutsideRootsRead:        PostureLight,
	api.GateUnobservedChannel:       PostureLight,
	api.GateUserRule:                PostureLight,
	api.GateExplicitApprovalRequest: PostureLight,

	api.GateAgentPolicyChange:           PostureBalanced,
	api.GateRemotePackageExecutionKnown: PostureBalanced,
	api.GateSensitiveLocation:           PostureBalanced,
	api.GateAgentChosenOutbound:         PostureBalanced,
	api.GateCapabilityWidening:          PostureBalanced,

	api.GateSecretExposedOutbound: PostureStrict,
	api.GateFirstHost:             PostureStrict,
	api.GateMCPUnleased:           PostureStrict,
}

// Enables reports whether this posture runs the given gate.
func (p Posture) Enables(g api.ApprovalGate) bool {
	floor, ok := gateFloor[g]
	return ok && p.Strictness() >= floor.Strictness()
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
