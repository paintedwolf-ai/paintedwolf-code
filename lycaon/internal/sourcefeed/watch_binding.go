package sourcefeed

import (
	"context"
	"github.com/lycaon/lycaon/internal/repochange"
	"log/slog"
	"strings"
)

// EnsureProjectWatch preserves unchanged bindings and their pending changes.
// A true result identifies a new binding that needs inventory catch-up.
func EnsureProjectWatch(ctx context.Context, owner *WatchOwner, projectID, scopeID string, roots []RootSpec, external ExternalObserver) bool {
	if owner != nil {
		ownedCtx, finish, err := owner.work.Begin(ctx)
		if err != nil {
			return false
		}
		defer finish()
		ctx = ownedCtx
	}
	projectID = strings.TrimSpace(projectID)
	cleaned := cleanWatchRoots(roots)
	if projectID == "" || len(cleaned) == 0 {
		return false
	}
	watchRegMu.Lock()
	// A canceled caller, such as a host that is shutting down, binds nothing:
	// its observer would outlive the host that releases the watches it bound.
	// The check holds the registry lock, so it orders against StopProjectWatch.
	if ctx.Err() != nil {
		watchRegMu.Unlock()
		return false
	}
	key := watchKey{projectID, scopeID}
	if owner != nil {
		external = owner.observer(key, external)
	}
	previous := watchers[key]
	if previous != nil && sameWatchRoots(previous.roots, cleaned) {
		previous.external = external
		previous.owner = owner
		for _, root := range cleaned {
			repochange.EnsureRoot(ctx, root.Path)
		}
		watchRegMu.Unlock()
		return false
	}
	if previous != nil {
		previous.unbind()
	}
	w := &projectWatch{projectID: projectID, roots: cleaned, external: external, owner: owner}
	w.changes = newChangeConverger(externalChangeQuietWindow, externalChangeMaxDelay, w.flushExternalChanges)
	w.changes.holdWhile(w.gitBusy, externalChangeHoldCeiling)
	w.unbind = repochange.RegisterObserver(w.observe)
	watchers[key] = w
	unusedRoots := unusedWatchRootsLocked(previous)
	for _, root := range unusedRoots {
		repochange.CloseRoot(root)
	}
	for _, root := range cleaned {
		repochange.EnsureRoot(ctx, root.Path)
	}
	watchRegMu.Unlock()
	if previous != nil {
		previous.changes.close(ctx)
	}
	paths := make([]string, 0, len(cleaned))
	for _, root := range cleaned {
		paths = append(paths, root.Path)
	}
	slog.InfoContext(ctx, "project source watch bound", "project_id", projectID, "roots", paths)
	return true
}
