package progress

import (
	"strings"
	"sync"
)

// completionArm emits one progress_complete per pending→terminal edge.
type completionArm struct {
	mu     sync.Mutex
	bySess map[string]*armState
}

type armState struct {
	emitted  bool
	reserved bool
	seq      int
}

var completions = &completionArm{bySess: make(map[string]*armState)}

// CompletionEmit is one reserved progress_complete row. Commit after insert; Abort to retry.
type CompletionEmit struct {
	sessionID string
	Seq       int
}

func (a *completionArm) state(sessionID string) *armState {
	st := a.bySess[sessionID]
	if st == nil {
		st = &armState{}
		a.bySess[sessionID] = st
	}
	return st
}

// ReserveCompletion arms the pending→terminal edge. allTerminal false re-arms.
func ReserveCompletion(sessionID string, allTerminal bool) (CompletionEmit, bool) {
	key := strings.TrimSpace(sessionID)
	if key == "" {
		return CompletionEmit{}, false
	}
	completions.mu.Lock()
	defer completions.mu.Unlock()
	st := completions.state(key)
	if !allTerminal {
		st.emitted = false
		st.reserved = false
		return CompletionEmit{}, false
	}
	if st.emitted || st.reserved {
		return CompletionEmit{}, false
	}
	st.reserved = true
	return CompletionEmit{sessionID: key, Seq: st.seq + 1}, true
}

// Commit records a landed transcript row.
func (e CompletionEmit) Commit() {
	key := strings.TrimSpace(e.sessionID)
	if key == "" {
		return
	}
	completions.mu.Lock()
	defer completions.mu.Unlock()
	st := completions.state(key)
	if !st.reserved {
		return
	}
	st.reserved = false
	st.emitted = true
	st.seq++
}

// Abort releases the reservation.
func (e CompletionEmit) Abort() {
	key := strings.TrimSpace(e.sessionID)
	if key == "" {
		return
	}
	completions.mu.Lock()
	defer completions.mu.Unlock()
	st := completions.state(key)
	if !st.reserved || st.emitted {
		return
	}
	st.reserved = false
}
