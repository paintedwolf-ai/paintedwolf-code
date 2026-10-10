package cadence

import (
	"context"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/workscope"
)

// ObserveRepochange registers repository notifications until the returned drain completes.
func (c *Service) ObserveRepochange() func(context.Context) error {
	if c == nil {
		return func(context.Context) error { return nil }
	}
	registration := &repochangeRegistration{observe: c.onRepochange}
	unregister := repochange.RegisterObserver(registration.deliver)
	return func(ctx context.Context) error {
		registration.work.Stop()
		unregister()
		if err := registration.work.Wait(ctx); err != nil {
			return err
		}
		registration.mu.Lock()
		registration.observe = nil
		registration.mu.Unlock()
		return nil
	}
}

type repochangeRegistration struct {
	work    workscope.Group
	mu      sync.Mutex
	observe repochange.Observer
}

func (o *repochangeRegistration) deliver(ctx context.Context, event repochange.Event) {
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
