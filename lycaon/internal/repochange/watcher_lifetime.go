package repochange

import (
	"context"
	"sync"
)

var watcherLifetimes struct {
	sync.Mutex
	leases  int
	current *WatcherRegistry
}

func processWatchers() *WatcherRegistry {
	watcherLifetimes.Lock()
	defer watcherLifetimes.Unlock()
	return processWatchersLocked()
}

func processWatchersLocked() *WatcherRegistry {
	if watcherLifetimes.current == nil {
		watcherLifetimes.current = NewWatcherRegistry()
	}
	return watcherLifetimes.current
}

// AcquireWatcherLifetime shares streams until the final engine retires their delivery.
func AcquireWatcherLifetime() func(context.Context) error {
	watcherLifetimes.Lock()
	processWatchersLocked()
	watcherLifetimes.leases++
	watcherLifetimes.Unlock()
	var once sync.Once
	var retired *WatcherRegistry
	return func(ctx context.Context) error {
		once.Do(func() {
			watcherLifetimes.Lock()
			watcherLifetimes.leases--
			if watcherLifetimes.leases == 0 {
				retired = watcherLifetimes.current
				watcherLifetimes.current = nil
			}
			watcherLifetimes.Unlock()
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
