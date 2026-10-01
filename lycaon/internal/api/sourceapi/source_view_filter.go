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
	previous := view.filtered
	view.filtered = nil
	view.treeIntent.Filter = strings.TrimSpace(query)
	view.filterGeneration = uuid.NewString()
	view.projectionRevision = uuid.NewString()
	view.filterDirty = true
	view.state = "preparing"
	view.failure = nil
	if view.treeIntent.Filter == "" && !view.reviewPreparing {
		view.state = "ready"
	}
	if view.filterCancel != nil {
		view.filterCancel()
	}
	return previous
}

func (s *Handler) treeViewChanged(view *sourceView) {
	s.refreshTreeFilter(view)
	view.notifier.Notify(false)
}

func (s *Handler) refreshTreeFilter(view *sourceView) {
	view.mu.Lock()
	if view.ctx.Err() != nil || view.treeIntent.Filter == "" || !view.treePrepared || view.reviewPreparing {
		view.mu.Unlock()
		return
	}
	view.filterDirty = true
	if view.filterRunning {
		view.mu.Unlock()
		return
	}
	view.filterRunning = true
	view.mu.Unlock()
	_, release, err := s.sourceViewRegistry().registry.Acquire(view.scope, view.id)
	if err != nil {
		view.mu.Lock()
		view.filterRunning = false
		view.mu.Unlock()
		return
	}
	s.background.Go(view.ctx, func(ctx context.Context) { defer release(); s.prepareTreeFilter(ctx, view) })
}

func (s *Handler) prepareTreeFilter(lifetime context.Context, view *sourceView) {
	for {
		view.mu.Lock()
		if view.ctx.Err() != nil || view.treeIntent.Filter == "" || view.reviewPreparing || !view.filterDirty {
			view.filterRunning = false
			view.filterCancel = nil
			view.mu.Unlock()
			return
		}
		query, generation := view.treeIntent.Filter, view.filterGeneration
		ctx, cancel := context.WithCancel(lifetime)
		view.filterCancel = cancel
		view.filterDirty = false
		view.mu.Unlock()
		filtered, err := view.tree.Filter(ctx, query)
		cancel()
		view.mu.Lock()
		if generation != view.filterGeneration || view.ctx.Err() != nil {
			view.mu.Unlock()
			if filtered != nil {
				filtered.Close()
			}
			continue
		}
		if errors.Is(err, pagedview.ErrRevision) || errors.Is(err, pagedview.ErrPreparing) {
			view.filterDirty = true
			view.mu.Unlock()
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-timer.C:
			case <-view.ctx.Done():
			}
			timer.Stop()
			continue
		}
		old := view.filtered
		if err == nil {
			view.filtered = filtered
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
