package progress

import (
	"context"
	"fmt"
	"strings"
)

const bootstrapGoalMaxRunes = 2000

type RunScopedStore interface {
	Store
	BoundRunID(sessionID string) string
}

// AdoptActiveRun preserves an unbound plan and resets a plan from another run.
func AdoptActiveRun(ctx context.Context, store Store, sessionID, runID, goal string) (bool, error) {
	return refreshRun(ctx, store, sessionID, runID, goal, false)
}

// EnsureBootstrap starts a fresh plan after completion or a run change.
func EnsureBootstrap(ctx context.Context, store Store, sessionID, runID, goal string) (bool, error) {
	return refreshRun(ctx, store, sessionID, runID, goal, true)
}

func refreshRun(ctx context.Context, store Store, sessionID, runID, goal string, bootstrap bool) (bool, error) {
	if store == nil || normalizeKey(sessionID) == "" || strings.TrimSpace(runID) == "" {
		return false, nil
	}
	var refreshed bool
	_, err := store.Update(ctx, sessionID, func(before Snapshot) (Document, error) {
		doc := Document{RunID: strings.TrimSpace(runID), Content: before.Content, ItemIDs: before.ItemIDs}
		refreshed = before.RunID != "" && (before.RunID != doc.RunID || (bootstrap && AllTerminal(before.Content)))
		if refreshed {
			doc.Content = formatBootstrapContent(goal)
			doc.ItemIDs = nil
		}
		return doc, nil
	})
	return refreshed && err == nil, err
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
