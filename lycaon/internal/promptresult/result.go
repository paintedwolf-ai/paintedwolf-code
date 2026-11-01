// Package promptresult names the outcome of running one prompt turn.
package promptresult

// TurnOutcome classifies how a prompt turn settled.
type TurnOutcome string

const (
	TurnOutcomeCompleted       TurnOutcome = "completed"
	TurnOutcomeCloseoutBlocked TurnOutcome = "closeout_blocked"
)

// CloseoutBlocked records why a turn stopped at a gated closeout while the
// workflow remains resumable at the named phase.
type CloseoutBlocked struct {
	Code           string   `json:"code"`
	Phase          string   `json:"phase,omitempty"`
	OpenGateLeaves []string `json:"open_gate_leaves,omitempty"`
	Notice         string   `json:"notice,omitempty"`
}

// Result identifies the assistant message or turn outcome a prompt turn produced.
type Result struct {
	MessageID string
	// OfferedTools are the tools the final model request could call. They are
	// not persisted: a replayed or recovered result carries none.
	OfferedTools    []string         `json:"-"`
	Outcome         TurnOutcome      `json:"outcome,omitempty"`
	CloseoutBlocked *CloseoutBlocked `json:"closeout_blocked,omitempty"`
}

// OfferedToolNames is nil-safe: a missing result offers no tools.
func (r *Result) OfferedToolNames() []string {
	if r == nil {
		return nil
	}
	return r.OfferedTools
}

// IsCloseoutBlocked reports whether the turn settled as closeout_blocked.
func (r *Result) IsCloseoutBlocked() bool {
	return r != nil && (r.Outcome == TurnOutcomeCloseoutBlocked || r.CloseoutBlocked != nil)
}
