package sourceapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcetree"
)

// changeTreeFilter runs under the view lock. Published rows from the previous
// query are retired before the accepted intent gets its new revision.
func (view *sourceView) changeTreeFilter(query string) *sourcetree.Filtered {
	previous := view.filtering.filtered
	view.filtering.filtered = nil
	view.navigation.treeIntent.Filter = strings.TrimSpace(query)
	view.filtering.filterGeneration = uuid.NewString()
	view.projectionRevision = uuid.NewString()
	view.filtering.filterDirty = true
	view.state = "preparing"
	view.failure = nil
	if view.navigation.treeIntent.Filter == "" && !view.reviewing.reviewPreparing {
		view.state = "ready"
	}
	if view.filtering.filterCancel != nil {
		view.filtering.filterCancel()
	}
	return previous
}

func (s *Trees) treeViewChanged(view *sourceView) {
	s.refreshTreeFilter(view)
	view.notifier.Notify(false)
}

func (s *Trees) refreshTreeFilter(view *sourceView) {
	view.mu.Lock()
	if view.ctx.Err() != nil || view.navigation.treeIntent.Filter == "" || !view.navigation.treePrepared || view.reviewing.reviewPreparing {
		view.mu.Unlock()
		return
	}
	view.filtering.filterDirty = true
	if view.filtering.filterRunning {
		view.mu.Unlock()
		return
	}
	view.filtering.filterRunning = true
	view.mu.Unlock()
	_, release, err := s.Views.sourceViewRegistry().registry.Acquire(view.scope, view.id)
	if err != nil {
		view.mu.Lock()
		view.filtering.filterRunning = false
		view.mu.Unlock()
		return
	}
	s.background.Go(view.ctx, func(ctx context.Context) { defer release(); s.prepareTreeFilter(ctx, view) })
}

func (s *Trees) prepareTreeFilter(lifetime context.Context, view *sourceView) {
	for {
		view.mu.Lock()
		if view.ctx.Err() != nil || view.navigation.treeIntent.Filter == "" || view.reviewing.reviewPreparing || !view.filtering.filterDirty {
			view.filtering.filterRunning = false
			view.filtering.filterCancel = nil
			view.mu.Unlock()
			return
		}
		query, generation := view.navigation.treeIntent.Filter, view.filtering.filterGeneration
		ctx, cancel := context.WithCancel(lifetime)
		view.filtering.filterCancel = cancel
		view.filtering.filterDirty = false
		view.mu.Unlock()
		filtered, err := view.navigation.tree.Filter(ctx, query)
		cancel()
		view.mu.Lock()
		if generation != view.filtering.filterGeneration || view.ctx.Err() != nil {
			view.mu.Unlock()
			if filtered != nil {
				filtered.Close()
			}
			continue
		}
		if errors.Is(err, pagedview.ErrRevision) || errors.Is(err, pagedview.ErrPreparing) {
			view.filtering.filterDirty = true
			view.mu.Unlock()
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-timer.C:
			case <-view.ctx.Done():
			}
			timer.Stop()
			continue
		}
		old := view.filtering.filtered
		if err == nil {
			view.filtering.filtered = filtered
			view.state = "ready"
			view.failure = nil
		} else {
			view.failLocked(err)
		}
		view.mu.Unlock()
		if err == nil && old != nil {
			old.Close()
		}
		view.notifier.Notify(true)
	}
}
