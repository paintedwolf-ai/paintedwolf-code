package approvalstate

import (
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/scopedstore"
)

// SandboxAskBegin is the outcome of consulting the spam guards before minting a card.
type SandboxAskBegin int

const (
	// SandboxAskMint reserves a new checkpoint.
	SandboxAskMint SandboxAskBegin = iota
	// SandboxAskJoin waits on the checkpoint already open for this key.
	SandboxAskJoin
	// SandboxAskSkipDenied marks a key the human already refused this turn.
	SandboxAskSkipDenied
	// SandboxAskSkipToolCall marks an invocation whose ask was already handled.
	SandboxAskSkipToolCall
)

// sandboxChatGrant identifies revocable authority; each capability checks its coverage.
type sandboxChatGrant interface {
	grantID() string
	grantCheckpointID() string
}

type sandboxGuardState[T sandboxChatGrant] struct {
	denySet      scopedstore.LRU[struct{}]
	pending      map[string]string // normalized key → checkpointID
	toolCallSeen scopedstore.LRU[struct{}]
	// grants holds human-approved grants, which carry an id and last for the
	// chat, and unnamed run grants the host derives while commands run.
	grants []T
}

// sandboxAskGuard coalesces pending asks and tracks denials per chat.
// Approvals install revocable grants that last until the chat is disposed.
type sandboxAskGuard[T sandboxChatGrant] struct {
	mu sync.Mutex
	// bySession is released only when the chat is disposed.
	bySession map[string]*sandboxGuardState[T]
	// overlayCap bounds each chat's unnamed run grants, oldest dropped first.
	// Human-approved grants are never trimmed.
	overlayCap int
	// normalizeKey canonicalizes a capability's key vocabulary. A capability
	// whose keys are already canonical leaves it nil.
	normalizeKey func(string) string
}

func (g *sandboxAskGuard[T]) key(raw string) string {
	if g.normalizeKey == nil {
		return raw
	}
	return g.normalizeKey(raw)
}

func (g *sandboxAskGuard[T]) state(sessionID string) *sandboxGuardState[T] {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "_"
	}
	st, ok := g.bySession[sessionID]
	if !ok {
		if g.bySession == nil {
			g.bySession = make(map[string]*sandboxGuardState[T])
		}
		st = &sandboxGuardState[T]{pending: make(map[string]string)}
		g.bySession[sessionID] = st
	}
	return st
}

// begin reserves an invocation before checkpoint creation.
func (g *sandboxAskGuard[T]) begin(sessionID, rawKey, toolCallID string) (SandboxAskBegin, string) {
	if g == nil {
		return SandboxAskMint, ""
	}
	key := g.key(rawKey)
	toolCallID = strings.TrimSpace(toolCallID)
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.state(sessionID)
	if key != "" {
		if _, denied := st.denySet.Load(key); denied {
			return SandboxAskSkipDenied, ""
		}
		if id, pending := st.pending[key]; pending && strings.TrimSpace(id) != "" {
			return SandboxAskJoin, id
		}
	}
	if toolCallID != "" {
		if _, seen := st.toolCallSeen.Load(toolCallID); seen {
			return SandboxAskSkipToolCall, ""
		}
		st.toolCallSeen.Store(toolCallID, struct{}{})
	}
	return SandboxAskMint, ""
}

// registerPending records the checkpoint now open for a key after a successful mint.
func (g *sandboxAskGuard[T]) registerPending(sessionID, rawKey, checkpointID string) {
	if g == nil {
		return
	}
	key := g.key(rawKey)
	checkpointID = strings.TrimSpace(checkpointID)
	if key == "" || checkpointID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state(sessionID).pending[key] = checkpointID
}

// abortMint clears a tool_call reservation when RequestCheckpoint fails after a mint.
func (g *sandboxAskGuard[T]) abortMint(sessionID, toolCallID string) {
	if g == nil {
		return
	}
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state(sessionID).toolCallSeen.Delete(toolCallID)
}

// finish clears pending after HITL resolves. A denial enters the deny set.
func (g *sandboxAskGuard[T]) finish(sessionID, rawKey string, denied bool) {
	if g == nil {
		return
	}
	key := g.key(rawKey)
	if key == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.state(sessionID)
	delete(st.pending, key)
	if denied {
		st.denySet.Store(key, struct{}{})
	}
}

// recordDenied leaves concurrent pending reviews untouched.
func (g *sandboxAskGuard[T]) recordDenied(sessionID, rawKey string) {
	if g == nil || g.key(rawKey) == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state(sessionID).denySet.Store(g.key(rawKey), struct{}{})
}

// clearDenied removes a key from the deny set after approval.
func (g *sandboxAskGuard[T]) clearDenied(sessionID, rawKey string) {
	if g == nil {
		return
	}
	key := g.key(rawKey)
	if key == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state(sessionID).denySet.Delete(key)
}

// A new user turn clears denials while preserving pending reviews.
func (g *sandboxAskGuard[T]) noteUserIntentBoundary(chatSessionID string) {
	if g == nil {
		return
	}
	chatSessionID = strings.TrimSpace(chatSessionID)
	if chatSessionID == "" {
		chatSessionID = "_"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if st, ok := g.bySession[chatSessionID]; ok && st != nil {
		st.denySet.Clear()
	}
}

// releaseRun drops what one run of the chat accumulated: open reviews,
// denials, and unnamed run grants. Human-approved grants survive Stop.
func (g *sandboxAskGuard[T]) releaseRun(sessionID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "_"
	}
	st, ok := g.bySession[sessionID]
	if !ok || st == nil {
		return
	}
	approved := make([]T, 0, len(st.grants))
	for _, grant := range st.grants {
		if grant.grantID() != "" {
			approved = append(approved, grant)
		}
	}
	if len(approved) == 0 {
		delete(g.bySession, sessionID)
		return
	}
	g.bySession[sessionID] = &sandboxGuardState[T]{pending: make(map[string]string), grants: approved}
}

// forgetSession releases every guard and grant for one session.
func (g *sandboxAskGuard[T]) forgetSession(sessionID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "_"
	}
	delete(g.bySession, sessionID)
}

// mergeGrants applies admission and the overlay limit under one lock.
func (g *sandboxAskGuard[T]) mergeGrants(sessionID string, merge func(existing []T) ([]T, bool)) bool {
	if g == nil || merge == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.state(sessionID)
	next, ok := merge(st.grants)
	if !ok {
		return false
	}
	st.grants = trimRunGrants(next, g.overlayCap)
	return true
}

// trimRunGrants drops the oldest unnamed run grants past limit.
func trimRunGrants[T sandboxChatGrant](grants []T, limit int) []T {
	unnamed := 0
	for _, grant := range grants {
		if grant.grantID() == "" {
			unnamed++
		}
	}
	over := unnamed - limit
	if over <= 0 {
		return grants
	}
	out := make([]T, 0, len(grants)-over)
	for _, grant := range grants {
		if over > 0 && grant.grantID() == "" {
			over--
			continue
		}
		out = append(out, grant)
	}
	return out
}

// readGrants lends the live overlay to the callback while holding the lock.
func (g *sandboxAskGuard[T]) readGrants(sessionID string, read func(grants []T)) {
	if g == nil || read == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	read(g.state(sessionID).grants)
}

// listGrants returns explicit approvals for one session.
func (g *sandboxAskGuard[T]) listGrants(sessionID string) []T {
	if g == nil {
		return nil
	}
	var out []T
	g.readGrants(sessionID, func(grants []T) {
		out = make([]T, 0, len(grants))
		for _, grant := range grants {
			if grant.grantID() != "" {
				out = append(out, grant)
			}
		}
	})
	return out
}

// listAllGrants returns every session's human-approved grants.
func (g *sandboxAskGuard[T]) listAllGrants() map[string][]T {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string][]T{}
	for sessionID, st := range g.bySession {
		if st == nil {
			continue
		}
		for _, grant := range st.grants {
			if grant.grantID() != "" {
				out[sessionID] = append(out[sessionID], grant)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// findByID locates a human-approved grant and its chat.
func (g *sandboxAskGuard[T]) findByID(id string) (T, string, bool) {
	var zero T
	id = strings.TrimSpace(id)
	if g == nil || id == "" {
		return zero, "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for sessionID, st := range g.bySession {
		if st == nil {
			continue
		}
		for _, grant := range st.grants {
			if grant.grantID() == id {
				return grant, sessionID, true
			}
		}
	}
	return zero, "", false
}

// revokeByID removes grants installed by one approval option.
func (g *sandboxAskGuard[T]) revokeByID(id, checkpointID string) (T, bool) {
	var removed T
	id = strings.TrimSpace(id)
	if g == nil || id == "" {
		return removed, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	found := false
	for _, st := range g.bySession {
		if st == nil {
			continue
		}
		next := make([]T, 0, len(st.grants))
		for _, grant := range st.grants {
			if grant.grantID() == id && (checkpointID == "" || grant.grantCheckpointID() == checkpointID) {
				if !found {
					removed = grant
					found = true
				}
				continue
			}
			next = append(next, grant)
		}
		st.grants = next
	}
	return removed, found
}

// AskGate is the guard surface a broker drives.
type AskGate interface {
	Begin(chatSessionID, key, toolCallID string) (SandboxAskBegin, string)
	RegisterPending(chatSessionID, key, checkpointID string)
	AbortMint(chatSessionID, toolCallID string)
	Finish(chatSessionID, key string, denied bool)
	ClearDenied(chatSessionID, key string)
	RecordDenied(chatSessionID, key string)
}
