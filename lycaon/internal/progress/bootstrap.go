package progress

import (
	"context"
	"fmt"
	"strings"
)

const bootstrapGoalMaxRunes = 2000

// RunScopedStore persists progress and the workflow run associated with the current document.
type RunScopedStore interface {
	Store
	BoundRunID(sessionID string) string
	BindRun(sessionID, workflowRunID string)
	EnsureRun(sessionID, workflowRunID, goal string) (refreshed bool)
}

// AdoptActiveRun binds the progress doc to workflowRunID on phase enter and
// child handoff. Unbound docs bind in place; a different run resets Goal + Progress.
// Same-run re-entry is a no-op (terminal same-run refresh is EnsureBootstrap).
func AdoptActiveRun(_ context.Context, store RunScopedStore, sessionID, workflowRunID, goal string) (refreshed bool) {
	if store == nil {
		return false
	}
	workflowRunID = strings.TrimSpace(workflowRunID)
	sessionID = normalizeKey(sessionID)
	if workflowRunID == "" || sessionID == "" {
		return false
	}
	stored := store.BoundRunID(sessionID)
	switch stored {
	case "":
		store.BindRun(sessionID, workflowRunID)
		return false
	case workflowRunID:
		return false
	default:
		store.EnsureRun(sessionID, workflowRunID, goal)
		BumpRevision(sessionID)
		return true
	}
}

// EnsureBootstrap refreshes Goal + Progress on a user-intent turn when the bound
// run changes, or when the same long-lived run has a fully terminal checklist.
// An unbound doc is adopted without wiping seeded content.
func EnsureBootstrap(ctx context.Context, store RunScopedStore, sessionID, workflowRunID, goal string) (refreshed bool) {
	if store == nil {
		return false
	}
	workflowRunID = strings.TrimSpace(workflowRunID)
	if workflowRunID == "" {
		return false
	}
	sessionID = normalizeKey(sessionID)
	if sessionID == "" {
		return false
	}
	stored := store.BoundRunID(sessionID)
	if stored == "" || stored != workflowRunID {
		return AdoptActiveRun(ctx, store, sessionID, workflowRunID, goal)
	}
	if !AllTerminal(store.Get(ctx, sessionID)) {
		return false
	}
	store.EnsureRun(sessionID, workflowRunID, goal)
	BumpRevision(sessionID)
	return true
}

func formatBootstrapContent(goal string) string {
	goalBody := strings.TrimSpace(goal)
	if goalBody == "" {
		goalBody = BootstrapPlaceholderGoal
	}
	if runes := []rune(goalBody); len(runes) > bootstrapGoalMaxRunes {
		goalBody = string(runes[:bootstrapGoalMaxRunes])
	}
	return fmt.Sprintf("# Goal\n\n%s\n\n## Progress\n", goalBody)
}
