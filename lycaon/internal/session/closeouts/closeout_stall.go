package closeouts

import (
	"context"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
)

// NoteCloseoutGroundingReject records a refused closeout draft and the
// members of it the report did not take.
func (m *Service) NoteCloseoutGroundingReject(ctx context.Context, sessionID, code, offenderKey, draftedContent string, unread []jsonshape.Issue) (attempt int, prevKey string) {
	if m == nil {
		return 0, ""
	}
	return m.noteGroundingReject(sessiontree.RootID(ctx, m.store, sessionID), code, offenderKey, draftedContent, unread)
}

func (c *Service) noteGroundingReject(rootID, code, offenderKey, draftedContent string, unread []jsonshape.Issue) (attempt int, prevKey string) {
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

// ResumeWork retires the pinned report without replenishing repair budgets.
// A tool attempt can change the conclusion even when it fails or is rejected.
func (m *Service) ResumeWork(ctx context.Context, sessionID string) {
	if m == nil {
		return
	}
	m.resumeWork(sessiontree.RootID(ctx, m.store, sessionID))
}

func (c *Service) resumeWork(rootID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cycle, ok := c.cycles.Load(rootID)
	if !ok {
		return
	}
	previous := cycle.stall
	cycle.stall = guidance.RetainedCloseout{
		Attempt: previous.Attempt, DocumentAttempt: previous.DocumentAttempt,
	}
	c.cycles.Store(rootID, cycle)
}

// CloseoutStallState returns a copy of the root's retained closeout repair.
func (m *Service) CloseoutStallState(ctx context.Context, sessionID string) guidance.RetainedCloseout {
	if m == nil {
		return guidance.RetainedCloseout{}
	}
	return m.stallState(sessiontree.RootID(ctx, m.store, sessionID))
}

func (c *Service) stallState(rootID string) guidance.RetainedCloseout {
	c.mu.Lock()
	defer c.mu.Unlock()
	cycle, _ := c.cycles.Load(rootID)
	state := cycle.stall
	state.ForcedBy = slices.Clone(state.ForcedBy)
	state.Unread = slices.Clone(state.Unread)
	return state
}

// ClearCloseoutStall clears citation recovery without replenishing retry budgets.
func (m *Service) ClearCloseoutStall(ctx context.Context, sessionID string) {
	if m == nil {
		return
	}
	m.clearStall(sessiontree.RootID(ctx, m.store, sessionID))
}

func (c *Service) clearStall(rootID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cycle, ok := c.cycles.Load(rootID)
	if !ok {
		return
	}
	cycle.stall = guidance.RetainedCloseout{}
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
