package session

import (
	"context"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/limits"
)

// closeoutStall keeps one closeout cycle's repair state and the tool turns
// taken since its latest refusal.
type closeoutStall struct {
	guidance.RetainedCloseout
	toolTurnsSince int
}

// NoteCloseoutGroundingReject records a refused closeout draft and the
// members of it the report did not take.
func (m *Manager) NoteCloseoutGroundingReject(ctx context.Context, sessionID, code, offenderKey, draftedContent string, unread []jsonshape.Issue) (attempt int, prevKey string) {
	if m == nil {
		return 0, ""
	}
	return m.closeout.noteGroundingReject(RootSessionID(ctx, m.store, sessionID), code, offenderKey, draftedContent, unread)
}

func (c *closeoutLifecycle) noteGroundingReject(rootID, code, offenderKey, draftedContent string, unread []jsonshape.Issue) (attempt int, prevKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cycle, _ := c.cycles.Load(rootID)
	entry := &cycle.stall
	prevKey = entry.PrevKey
	entry.Active = true
	if guidance.ReportDocumentObservation(code) != "" {
		entry.DocumentAttempt++
	} else {
		entry.Attempt++
	}
	entry.PrevKey = offenderKey
	entry.toolTurnsSince = 0
	if code = strings.TrimSpace(code); code != "" {
		entry.ForcedBy = appendDistinctCode(entry.ForcedBy, code)
	}
	if s := strings.TrimSpace(draftedContent); s != "" {
		entry.Drafted = guidance.RetainCloseoutDraft(entry.Drafted, s, guidance.RepairsReportDocument(entry.ForcedBy))
	}
	entry.Unread = slices.Clone(unread)
	c.cycles.Store(rootID, cycle)
	return entry.Attempt + entry.DocumentAttempt, prevKey
}

// NoteCoordinatorToolTurn counts tool turns only while citation recovery is active.
func (m *Manager) NoteCoordinatorToolTurn(ctx context.Context, sessionID string) (tripped bool) {
	if m == nil {
		return false
	}
	return m.closeout.noteToolTurn(RootSessionID(ctx, m.store, sessionID))
}

func (c *closeoutLifecycle) noteToolTurn(rootID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	cycle, _ := c.cycles.Load(rootID)
	entry := &cycle.stall
	if !entry.Active {
		return false
	}
	entry.toolTurnsSince++
	c.cycles.Store(rootID, cycle)
	return entry.toolTurnsSince >= limits.DefaultCloseoutStallToolTurns
}

// CloseoutStallState returns a copy of the root's retained closeout repair.
func (m *Manager) CloseoutStallState(ctx context.Context, sessionID string) guidance.RetainedCloseout {
	if m == nil {
		return guidance.RetainedCloseout{}
	}
	return m.closeout.stallState(RootSessionID(ctx, m.store, sessionID))
}

func (c *closeoutLifecycle) stallState(rootID string) guidance.RetainedCloseout {
	c.mu.Lock()
	defer c.mu.Unlock()
	cycle, _ := c.cycles.Load(rootID)
	state := cycle.stall.RetainedCloseout
	state.ForcedBy = slices.Clone(state.ForcedBy)
	state.Unread = slices.Clone(state.Unread)
	return state
}

// ClearCloseoutStall clears citation recovery without replenishing retry budgets.
func (m *Manager) ClearCloseoutStall(ctx context.Context, sessionID string) {
	if m == nil {
		return
	}
	m.closeout.clearStall(RootSessionID(ctx, m.store, sessionID))
}

func (c *closeoutLifecycle) clearStall(rootID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cycle, ok := c.cycles.Load(rootID)
	if !ok {
		return
	}
	cycle.stall = closeoutStall{}
	c.cycles.Store(rootID, cycle)
}

func appendDistinctCode(codes []string, code string) []string {
	for _, c := range codes {
		if c == code {
			return codes
		}
	}
	return append(codes, code)
}
