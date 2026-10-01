package gate

import "github.com/lycaon/lycaon/pkg/api"

// All returns every gate in citation-priority order, which ranks a card's reasons.
func All() []api.ApprovalGate { return api.AllApprovalGateValues() }

// IsKnown reports whether g is in the closed gate vocabulary.
func IsKnown(g api.ApprovalGate) bool {
	for _, candidate := range api.AllApprovalGateValues() {
		if candidate == g {
			return true
		}
	}
	return false
}

// rank orders gates for primary selection. Unknown gates sort last so a member
// that reached the host without a place in the vocabulary cannot outrank a real one.
func rank(g api.ApprovalGate) int {
	all := api.AllApprovalGateValues()
	for i, candidate := range all {
		if candidate == g {
			return i
		}
	}
	return len(all)
}

// Scope is the temporal width of reusable authority a gate may offer. It is a
// separate question from subject shape (see Reuse): an effect can be worth
// reusing for a chat without being worth reusing for a month.
type Scope string

const (
	// ScopeNone means no reusable authority is offered for this gate.
	ScopeNone Scope = ""
	// ScopeChat lasts until the chat is deleted.
	ScopeChat Scope = "chat"
	// ScopeProject is durable for this project.
	ScopeProject Scope = "project"
	// ScopeDevice is durable for this project on this device.
	ScopeDevice Scope = "device"
)

// Durable reports whether this scope outlives the chat that granted it.
func (s Scope) Durable() bool { return s == ScopeProject || s == ScopeDevice }

// IsFilesystem reports whether g gates a filesystem path.
func IsFilesystem(g api.ApprovalGate) bool {
	return g == api.GateSensitiveLocation || g == api.GateOutsideRootsWrite || g == api.GateOutsideRootsRead
}
