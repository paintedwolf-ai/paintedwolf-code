package sourceapi

import (
	"context"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcefeed"
)

func (s *Trees) watchTreeView(view *sourceView, p *project.Project) {
	refresher := &treeRefresher{background: s.background, ctx: view.ctx, wake: make(chan struct{}, 1), quiet: 750 * time.Millisecond, maximum: 5 * time.Second,
		refresh: func() {
			view.mu.Lock()
			if view.navigation.treeIntent.Review != nil {
				view.reviewing.reviewDirty = true
			}
			view.mu.Unlock()
			s.refreshTreeReview(view)
		}}
	unsubscribe := sourcefeed.Subscribe(p.ID, p.WorkspaceID(), func(notice sourcefeed.Notice) {
		if !notice.FileWritesOnly {
			refresher.notify()
		}
	})
	context.AfterFunc(view.ctx, unsubscribe)
	s.background.Go(view.ctx, func(ctx context.Context) { s.Watch.ensureWorkspaceWatch(ctx, p) })
}

// treeRefresher refreshes a tree view once per quiet burst of changes. Each
// burst is tracked background work, so draining the background covers it.
type treeRefresher struct {
	background     *taskgroup.Group
	ctx            context.Context
	wake           chan struct{}
	quiet, maximum time.Duration
	refresh        func()

	mu         sync.Mutex
	refreshing bool
}

func (r *treeRefresher) notify() {
	// The wake is sent before the check, so a finishing burst sees it.
	select {
	case r.wake <- struct{}{}:
	default:
	}
	r.mu.Lock()
	start := !r.refreshing
	r.refreshing = true
	r.mu.Unlock()
	if start {
		r.background.Go(r.ctx, r.run)
	}
}

func (r *treeRefresher) run(ctx context.Context) {
	// Tracked work outlives its parent's cancellation; the view's end stops it.
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	defer context.AfterFunc(r.ctx, stop)()
	for {
		select {
		case <-r.wake:
		default:
		}
		if !waitTreeReviewQuiet(ctx, r.wake, r.quiet, r.maximum) {
			r.mu.Lock()
			r.refreshing = false
			r.mu.Unlock()
			return
		}
		r.refresh()
		r.mu.Lock()
		select {
		case <-r.wake:
			r.mu.Unlock()
			continue
		default:
		}
		r.refreshing = false
		r.mu.Unlock()
		return
	}
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
