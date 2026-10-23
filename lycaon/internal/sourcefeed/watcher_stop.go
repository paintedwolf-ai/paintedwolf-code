package sourcefeed

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/repochange"
)

// Draining watches remain reachable when a caller cancels its wait.
var drainingWatches = map[*projectWatch]struct{}{}

// StopProjectWatch removes project routing and drains admitted callbacks.
func StopProjectWatch(ctx context.Context, projectID string) error {
	projectID = strings.TrimSpace(projectID)
	watchRegMu.Lock()
	var removed []*projectWatch
	for key, watch := range watchers {
		if key.projectID == projectID {
			watch.callbacks.Seal()
			drainingWatches[watch] = struct{}{}
			removed = append(removed, watch)
			delete(watchers, key)
		}
	}
	var draining []*projectWatch
	for watch := range drainingWatches {
		if watch.projectID == projectID {
			draining = append(draining, watch)
		}
	}
	var unused []string
	for _, watch := range removed {
		unused = append(unused, unusedWatchRootsLocked(watch)...)
	}
	watchRegMu.Unlock()
	for _, watch := range removed {
		watch.unbind()
		watch.changes.close(ctx)
	}
	var drainErr error
	for _, watch := range draining {
		if err := watch.callbacks.Wait(ctx); err != nil {
			drainErr = errors.Join(drainErr, err)
			continue
		}
		watchRegMu.Lock()
		delete(drainingWatches, watch)
		watchRegMu.Unlock()
	}
	for _, root := range unused {
		repochange.CloseRoot(root)
	}
	return drainErr
}
