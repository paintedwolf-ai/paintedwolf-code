package agentpresence

import (
	"context"
	"slices"

	"github.com/lycaon/lycaon/pkg/api"
)

// staleCheck is one chat item whose recorded text may have changed.
type staleCheck struct {
	chatSessionID string
	id            string
	// wholeFile items have no spans; any foreign change after their revision marks them.
	wholeFile bool
	revision  int64
	spans     []AnchoredSpan
}

// documentChanged re-checks the reads anchored in a document after its content
// changed. A change the chat itself is making or just landed does not mark that
// chat's items.
func (t *Tracker) documentChanged(ctx context.Context, projectID, documentID string, revision int64) {
	if t.anchors == nil {
		return
	}
	checks := t.staleChecks(projectID, documentID, revision)
	if len(checks) == 0 {
		return
	}
	stale := make(map[string]bool, len(checks))
	for _, check := range checks {
		if check.wholeFile {
			stale[check.id] = revision > check.revision
			continue
		}
		held, err := t.anchors.SpansHold(ctx, projectID, documentID, check.spans)
		if err != nil || len(held) != len(check.spans) {
			continue
		}
		stale[check.id] = slices.Contains(held, false)
	}
	for _, sessionID := range chatIDs(checks) {
		t.markStale(ctx, projectID, sessionID, stale)
	}
}

func (t *Tracker) staleChecks(projectID, documentID string, revision int64) []staleCheck {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.projects[projectID]
	if p == nil {
		return nil
	}
	var checks []staleCheck
	for sessionID, c := range p.chats {
		if c.landed[documentID] >= revision || slices.ContainsFunc(c.intents, func(i api.AgentIntent) bool { return i.DocumentID == documentID }) {
			continue
		}
		for _, r := range c.reads {
			if r.document == documentID && !r.wire.Stale {
				checks = append(checks, staleCheck{chatSessionID: sessionID, id: r.wire.ID, wholeFile: len(r.spans) == 0, revision: r.revision, spans: slices.Clone(r.spans)})
			}
		}
	}
	return checks
}

func (t *Tracker) markStale(ctx context.Context, projectID, sessionID string, stale map[string]bool) {
	t.mutate(ctx, ChatRef{ProjectID: projectID, SessionID: sessionID}, func(c *chatPresence) bool {
		changed := false
		for _, r := range c.reads {
			if stale[r.wire.ID] && !r.wire.Stale {
				r.wire.Stale, changed = true, true
			}
		}
		return changed
	})
}

func chatIDs(checks []staleCheck) []string {
	var ids []string
	for _, check := range checks {
		if !slices.Contains(ids, check.chatSessionID) {
			ids = append(ids, check.chatSessionID)
		}
	}
	return ids
}
