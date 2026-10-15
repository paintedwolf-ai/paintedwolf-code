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
	registration := &repochangeRegistration{service: c}
	unregister := repochange.RegisterObserver(registration.deliver)
	return func(ctx context.Context) error {
		registration.work.Stop()
		unregister()
		if err := registration.work.Wait(ctx); err != nil {
			return err
		}
		registration.mu.Lock()
		registration.service = nil
		registration.mu.Unlock()
		return nil
	}
}

type repochangeRegistration struct {
	work    workscope.Group
	mu      sync.Mutex
	service *Service
}

func (o *repochangeRegistration) deliver(ctx context.Context, event repochange.Event) {
	ctx, finish, err := o.work.Begin(ctx)
	if err != nil {
		return
	}
	defer finish()
	if event.Kind != repochange.WorktreeChanged || len(event.Paths) == 0 {
		return
	}
	o.mu.Lock()
	service := o.service
	o.mu.Unlock()
	if service == nil {
		return
	}
	if err := service.NoteWrites(ctx, event.ProjectDir, event.Paths); err != nil {
		slog.WarnContext(ctx, "scan cadence note writes", "path", event.ProjectDir, "error", err)
	}
}
