package approvalstate

import (
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/scopedstore"
)

// joinedToolCallIDCap bounds joined_tool_call_ids on the wire payload; joined_count
// may still grow past this.
const joinedToolCallIDCap = 32

// ToolApprovalCoalesce tracks pending reviews and exact-action denials per chat.
type ToolApprovalCoalesce struct {
	mu sync.Mutex
	// byChat bounds per-session approval state; ForgetSession releases it.
	byChat scopedstore.LRU[*toolApprovalGuards]
}

type toolApprovalGuards struct {
	denySet map[string]struct{}
	pending map[string]*toolApprovalPending // grantKey → entry
}

type toolApprovalPending struct {
	checkpointID string
	joinedCount  int
	toolCallIDs  []string
	// ready closes when card creation resolves. Reserving first coalesces concurrent asks.
	ready chan struct{}
}

// ToolApprovalCoalesceBegin is the outcome of consulting spam guards before minting a card.
type ToolApprovalCoalesceBegin int

const (
	// ToolApprovalCoalesceMint reserves a new card: RequestCheckpoint, then RegisterPending.
	ToolApprovalCoalesceMint ToolApprovalCoalesceBegin = iota
	// ToolApprovalCoalesceJoin waits on the card already open for grantKey.
	ToolApprovalCoalesceJoin
	// ToolApprovalCoalesceSkipDenied marks a grantKey in the deny set; the tool fails.
	ToolApprovalCoalesceSkipDenied
)

// NewToolApprovalCoalesce constructs empty guard state.
func NewToolApprovalCoalesce() *ToolApprovalCoalesce {
	return &ToolApprovalCoalesce{}
}

func (r *ToolApprovalCoalesce) guards(chatSessionID string) *toolApprovalGuards {
	chatSessionID = normalizeChat(chatSessionID)
	g, ok := r.byChat.Load(chatSessionID)
	if !ok {
		g = &toolApprovalGuards{
			denySet: make(map[string]struct{}),
			pending: make(map[string]*toolApprovalPending),
		}
		r.byChat.Store(chatSessionID, g)
	}
	return g
}

func normalizeChat(chatSessionID string) string {
	chatSessionID = strings.TrimSpace(chatSessionID)
	if chatSessionID == "" {
		return "_"
	}
	return chatSessionID
}

// Begin consults the deny set and exact pending action identity. A Mint caller
// follows RequestCheckpoint with RegisterPending, or AbortMint if the request fails.
func (r *ToolApprovalCoalesce) Begin(chatSessionID, grantKey string) (ToolApprovalCoalesceBegin, string) {
	if r == nil {
		return ToolApprovalCoalesceMint, ""
	}
	grantKey = strings.TrimSpace(grantKey)
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.guards(chatSessionID)
	if grantKey != "" {
		if _, denied := g.denySet[grantKey]; denied {
			return ToolApprovalCoalesceSkipDenied, ""
		}
		if entry, pending := g.pending[grantKey]; pending && entry != nil {
			if id := strings.TrimSpace(entry.checkpointID); id != "" {
				return ToolApprovalCoalesceJoin, id
			}
			// Wait for the matching card’s ID before reserving another.
			ready := entry.ready
			r.mu.Unlock()
			waitForMintReservation(ready)
			r.mu.Lock()
			if cur, ok := r.guards(chatSessionID).pending[grantKey]; ok && cur != nil {
				if id := strings.TrimSpace(cur.checkpointID); id != "" {
					return ToolApprovalCoalesceJoin, id
				}
			}
			// Mint aborted — this caller takes over and reserves the next mint.
			r.guards(chatSessionID).pending[grantKey] = newMintReservation()
			return ToolApprovalCoalesceMint, ""
		}
	}
	if grantKey != "" {
		g.pending[grantKey] = newMintReservation()
	}
	return ToolApprovalCoalesceMint, ""
}

// mintWaitTimeout allows recovery from abandoned card creation.
const mintWaitTimeout = 10 * time.Second

func newMintReservation() *toolApprovalPending {
	return &toolApprovalPending{ready: make(chan struct{})}
}

// Card creation waits outside r.mu so registration can acquire the lock.
func waitForMintReservation(ready chan struct{}) {
	if ready == nil {
		return
	}
	timer := time.NewTimer(mintWaitTimeout)
	defer timer.Stop()
	select {
	case <-ready:
	case <-timer.C:
	}
}

// RegisterPending records checkpointID for grantKey after a successful mint.
func (r *ToolApprovalCoalesce) RegisterPending(chatSessionID, grantKey, checkpointID, toolCallID string) {
	if r == nil {
		return
	}
	grantKey = strings.TrimSpace(grantKey)
	checkpointID = strings.TrimSpace(checkpointID)
	toolCallID = strings.TrimSpace(toolCallID)
	if grantKey == "" || checkpointID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.guards(chatSessionID)
	entry := &toolApprovalPending{
		checkpointID: checkpointID,
		joinedCount:  1,
	}
	if toolCallID != "" {
		entry.toolCallIDs = []string{toolCallID}
	}
	// Carry the reservation's channel so waiters are released by this fill.
	if prev, ok := g.pending[grantKey]; ok && prev != nil && prev.ready != nil {
		entry.ready = prev.ready
	} else {
		entry.ready = make(chan struct{})
	}
	g.pending[grantKey] = entry
	closeReady(entry.ready)
}

// RestorePending reconstructs a persisted pending entry after restart.
// Persisted entries contain joined diagnostics and no mint reservation.
func (r *ToolApprovalCoalesce) RestorePending(chatSessionID, grantKey, checkpointID string, joinedCount int, toolCallIDs []string) {
	if r == nil {
		return
	}
	grantKey = strings.TrimSpace(grantKey)
	checkpointID = strings.TrimSpace(checkpointID)
	if grantKey == "" || checkpointID == "" {
		return
	}
	if joinedCount < 1 {
		joinedCount = 1
	}
	ids := append([]string(nil), toolCallIDs...)
	if len(ids) > joinedToolCallIDCap {
		ids = ids[:joinedToolCallIDCap]
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.guards(chatSessionID).pending[grantKey] = &toolApprovalPending{
		checkpointID: checkpointID,
		joinedCount:  joinedCount,
		toolCallIDs:  ids,
		ready:        closedReady(),
	}
}

func closedReady() chan struct{} {
	ready := make(chan struct{})
	close(ready)
	return ready
}

// closeReady closes a reservation channel at most once.
func closeReady(ready chan struct{}) {
	if ready == nil {
		return
	}
	select {
	case <-ready:
	default:
		close(ready)
	}
}

// AbortMint clears an unfilled exact-action reservation after Begin returned Mint.
func (r *ToolApprovalCoalesce) AbortMint(chatSessionID, grantKey string) {
	if r == nil {
		return
	}
	grantKey = strings.TrimSpace(grantKey)
	if grantKey == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.guards(chatSessionID)
	if entry, ok := g.pending[grantKey]; ok && entry != nil && strings.TrimSpace(entry.checkpointID) == "" {
		closeReady(entry.ready)
		delete(g.pending, grantKey)
	}
}

// ClearPending drops the pending entry for grantKey after the mint checkpoint resolves.
func (r *ToolApprovalCoalesce) ClearPending(chatSessionID, grantKey string) {
	if r == nil {
		return
	}
	grantKey = strings.TrimSpace(grantKey)
	if grantKey == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.guards(chatSessionID)
	if entry, ok := g.pending[grantKey]; ok && entry != nil {
		closeReady(entry.ready)
	}
	delete(g.pending, grantKey)
}

// RecordDeny inserts grantKey into the chat deny-set (after Deny only).
func (r *ToolApprovalCoalesce) RecordDeny(chatSessionID, grantKey string) {
	if r == nil {
		return
	}
	grantKey = strings.TrimSpace(grantKey)
	if grantKey == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.guards(chatSessionID).denySet[grantKey] = struct{}{}
}

// ClearDeny removes grantKey from the deny-set after approval.
func (r *ToolApprovalCoalesce) ClearDeny(chatSessionID, grantKey string) {
	if r == nil {
		return
	}
	grantKey = strings.TrimSpace(grantKey)
	if grantKey == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.guards(chatSessionID).denySet, grantKey)
}

// ForgetSession clears pending actions and the exact-action deny set for the chat.
func (r *ToolApprovalCoalesce) ForgetSession(chatSessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byChat.Delete(normalizeChat(chatSessionID))
}

// NoteUserIntentBoundary clears the chat's deny-set on a new user turn.
// Pending entries are untouched — an open card stays open across turns.
func (r *ToolApprovalCoalesce) NoteUserIntentBoundary(chatSessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.guards(chatSessionID)
	g.denySet = make(map[string]struct{})
}

// NoteJoin records a joiner for the pending grantKey and returns the new joined_count.
func (r *ToolApprovalCoalesce) NoteJoin(chatSessionID, grantKey, toolCallID string) int {
	if r == nil {
		return 0
	}
	grantKey = strings.TrimSpace(grantKey)
	toolCallID = strings.TrimSpace(toolCallID)
	if grantKey == "" {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.guards(chatSessionID).pending[grantKey]
	if !ok || entry == nil {
		return 0
	}
	entry.joinedCount++
	if toolCallID != "" && len(entry.toolCallIDs) < joinedToolCallIDCap {
		entry.toolCallIDs = append(entry.toolCallIDs, toolCallID)
	}
	return entry.joinedCount
}

// JoinedCount returns the current waiter count for a pending grantKey (0 if none).
func (r *ToolApprovalCoalesce) JoinedCount(chatSessionID, grantKey string) int {
	if r == nil {
		return 0
	}
	grantKey = strings.TrimSpace(grantKey)
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.guards(chatSessionID).pending[grantKey]
	if !ok || entry == nil {
		return 0
	}
	return entry.joinedCount
}

// JoinedToolCallIDs returns a copy of the capped tool_call id list for a pending key.
func (r *ToolApprovalCoalesce) JoinedToolCallIDs(chatSessionID, grantKey string) []string {
	if r == nil {
		return nil
	}
	grantKey = strings.TrimSpace(grantKey)
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.guards(chatSessionID).pending[grantKey]
	if !ok || entry == nil || len(entry.toolCallIDs) == 0 {
		return nil
	}
	return append([]string(nil), entry.toolCallIDs...)
}
