package cadence

import (
	"context"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/workscope"
)

// ObserveRepochange owns repository notifications until the returned drain completes.
func (c *Service) ObserveRepochange() func(context.Context) error {
	if c == nil {
		return func(context.Context) error { return nil }
	}
	owner := &repochangeOwner{observe: c.onRepochange}
	unregister := repochange.RegisterObserver(owner.deliver)
	return func(ctx context.Context) error {
		owner.work.Stop()
		unregister()
		if err := owner.work.Wait(ctx); err != nil {
			return err
		}
		owner.mu.Lock()
		owner.observe = nil
		owner.mu.Unlock()
		return nil
	}
}

type repochangeOwner struct {
	work    workscope.Group
	mu      sync.Mutex
	observe repochange.Observer
}

func (o *repochangeOwner) deliver(ctx context.Context, event repochange.Event) {
	ctx, finish, err := o.work.Begin(ctx)
	if err != nil {
		return
	}
	defer finish()
	o.mu.Lock()
	observe := o.observe
	o.mu.Unlock()
	observe(ctx, event)
}

func (c *Service) onRepochange(ctx context.Context, event repochange.Event) {
	if event.Kind != repochange.WorktreeChanged || len(event.Paths) == 0 {
		return
	}
	if err := c.NoteWrites(ctx, event.ProjectDir, event.Paths); err != nil {
		slog.WarnContext(ctx, "scan cadence note writes", "path", event.ProjectDir, "error", err)
	}
}
