package sourceapi

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcefeed"
)

func (s *Trees) watchTreeView(view *sourceView, p *project.Project) {
	wake := make(chan struct{}, 1)
	unsubscribe := sourcefeed.Subscribe(p.ID, p.WorkspaceID(), func(notice sourcefeed.Notice) {
		if notice.FileWritesOnly {
			return
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	s.background.Go(view.ctx, func(ctx context.Context) { s.Watch.ensureWorkspaceWatch(ctx, p) })
	s.background.Go(view.ctx, func(ctx context.Context) {
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}
			if !waitTreeReviewQuiet(ctx, wake, 750*time.Millisecond, 5*time.Second) {
				return
			}
			view.mu.Lock()
			if view.navigation.treeIntent.Review != nil {
				view.reviewing.reviewDirty = true
			}
			view.mu.Unlock()
			s.refreshTreeReview(view)
		}
	})
}

// Structural bursts share one ledger refresh; continuous work still converges.
func waitTreeReviewQuiet(ctx context.Context, wake <-chan struct{}, quiet, maximum time.Duration) bool {
	settled, deadline := time.NewTimer(quiet), time.NewTimer(maximum)
	defer settled.Stop()
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return true
		case <-settled.C:
			return true
		case <-wake:
			settled.Reset(quiet)
		}
	}
}
