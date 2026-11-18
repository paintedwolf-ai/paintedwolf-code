package session

import (
	"github.com/lycaon/lycaon/internal/session/decisions"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
)

// SetDecisionStore wires pending worker decisions.
func (m *Host) SetDecisionStore(store decisions.*sessionstore.Repositories) {
	if m != nil {
		m.Decisions = store
		m.Workers.SetDecisions(store)
		m.Workers.Summaries.SetDecisions(store)
		m.Workers.Results.SetDecisions(store)
	}
}

// Decisions returns the pending worker decision store.
