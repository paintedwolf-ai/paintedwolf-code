package repochange

import (
	"context"
	"sync"
)

type streamLifetimes struct {
	sync.Mutex
	leases  int
	retired bool
	current *watcherRegistry
}

var watcherLifetimes streamLifetimes

func processWatchers() *watcherRegistry { return watcherLifetimes.registry() }

func (l *streamLifetimes) registry() *watcherRegistry {
	l.Lock()
	defer l.Unlock()
	return l.registryLocked()
}

func (l *streamLifetimes) registryLocked() *watcherRegistry {
	if l.current == nil {
		l.current = newWatcherRegistry()
	}
	return l.current
}

// AcquireWatcherLifetime shares streams until the final engine retires their delivery.
func AcquireWatcherLifetime() func(context.Context) error { return watcherLifetimes.acquire() }

func (l *streamLifetimes) acquire() func(context.Context) error {
	l.Lock()
	if l.retired {
		l.current = newWatcherRegistry()
		l.retired = false
	}
	l.registryLocked()
	l.leases++
	l.Unlock()
	var once sync.Once
	var retired *watcherRegistry
	return func(ctx context.Context) error {
		once.Do(func() {
			l.Lock()
			l.leases--
			if l.leases == 0 {
				retired = l.current
				l.retired = true
			}
			l.Unlock()
			if retired != nil {
				retired.CloseAll()
			}
		})
		if retired == nil {
			return nil
		}
		if err := retired.work.Wait(ctx); err != nil {
			return err
		}
		return retired.debounce.retire(ctx)
	}
}
