package gate

import (
	"slices"

	"github.com/lycaon/lycaon/pkg/api"
)

// ReuseShape is how narrow the subject of a reusable answer is.
type ReuseShape string

const (
	// ReuseNone means no reusable authority is offered.
	ReuseNone ReuseShape = ""
	// ReuseExactAction leases only the byte-identical action under an unchanged boundary.
	ReuseExactAction ReuseShape = "exact_action"
	// ReusePredicate leases a family predicate (host, path, MCP tool, …).
	ReusePredicate ReuseShape = "predicate"
)

// Reuse is the reusable authority a gate may offer: subject shape × temporal scope.
type Reuse struct {
	Shape ReuseShape
	Scope Scope
	// DayCarrier is the scope the one-day rung rides when it differs from Scope.
	DayCarrier Scope
}

// DayScope is the scope the one-day rung rides.
func (r Reuse) DayScope() Scope {
	if r.DayCarrier != ScopeNone {
		return r.DayCarrier
	}
	return r.Scope
}

// Offered reports whether any reusable authority is on offer.
func (r Reuse) Offered() bool {
	return r.Shape != ReuseNone && r.Scope != ScopeNone
}

// ReuseFor returns the reusable authority offered by a gate.
func ReuseFor(g api.ApprovalGate) Reuse {
	switch g {
	case api.GateConsentDrift, api.GateIncompleteFacts:
		return Reuse{Shape: ReuseExactAction, Scope: ScopeProject}
	case api.GateRemotePackageExecution, api.GateRemotePackageExecutionKnown:
		return Reuse{Shape: ReusePredicate, Scope: ScopeProject}
	case api.GateAuthorityMisuse:
		return Reuse{Shape: ReuseExactAction, Scope: ScopeProject}
	case api.GateSecretOutbound:
		// Fingerprints provide stable subjects without retaining values.
		return Reuse{Shape: ReusePredicate, Scope: ScopeProject}
	case api.GateUnobservedChannel:
		// Reuse stays bound to the exact unobserved channel.
		return Reuse{Shape: ReusePredicate, Scope: ScopeProject}
	case api.GateAgentChosenOutbound, api.GateFirstHost, api.GateMCPUnleased:
		return Reuse{Shape: ReusePredicate, Scope: ScopeProject}
	case api.GateSecretExposedOutbound:
		// Exposure reuse is keyed to the destination host.
		return Reuse{Shape: ReusePredicate, Scope: ScopeProject}
	case api.GateSensitiveLocation, api.GateOutsideRootsWrite, api.GateOutsideRootsRead:
		// Sensitive locations are machine-level subjects.
		return Reuse{Shape: ReusePredicate, Scope: ScopeDevice}
	case api.GateUserRule:
		return Reuse{Shape: ReusePredicate, Scope: ScopeDevice}
	case api.GateExplicitApprovalRequest:
		// Explicit requests reuse only the enumerated action set.
		return Reuse{Shape: ReuseExactAction, Scope: ScopeProject}
	case api.GateCapabilityWidening:
		// Axis subject; day rides the chat because the runtime is chat-scoped.
		return Reuse{Shape: ReusePredicate, Scope: ScopeChat}
	case api.GateAgentPolicyChange:
		// Trust-surface or exact-file subject; standing authority lasts only for the chat.
		return Reuse{Shape: ReusePredicate, Scope: ScopeChat}
	default:
		return Reuse{}
	}
}

// HostResourceReuse is the authority a host-resource ladder may offer: the
// named catalog ids, up to device. Capability cards use this even when the
// primary gate's ceiling is narrower, so a device host-resource lease can
// cover that resource's realization on a later project.
func HostResourceReuse() Reuse {
	return Reuse{Shape: ReusePredicate, Scope: ScopeDevice}
}

// OnlyDirectIPChannel reports whether direct networking is the decision's
// sole reason. A direct-IP chat lease answers that reason and nothing else,
// so a detection, user rule, or unapplied boundary on the same action asks.
func (d *Decision) OnlyDirectIPChannel() bool {
	if d == nil {
		return false
	}
	for _, g := range d.Gates() {
		if g != api.GateUnobservedChannel {
			return false
		}
	}
	for _, cited := range d.Cited {
		if cited.Key == "boundary.egress" && cited.Value == EgressDirectIP {
			return true
		}
	}
	return false
}

// OffersQuiet reports whether a decision's reasons may be quieted. A change
// to agent policy is covered only by a lease naming its subject.
func (d *Decision) OffersQuiet() bool {
	return !slices.Contains(d.Gates(), api.GateAgentPolicyChange)
}

// ExactActionQuiet keys quiet to the byte-identical action rather than the
// reason class.
func ExactActionQuiet(g api.ApprovalGate) bool {
	switch g {
	case api.GateConsentDrift, api.GateIncompleteFacts, api.GateExplicitApprovalRequest:
		return true
	default:
		return false
	}
}
