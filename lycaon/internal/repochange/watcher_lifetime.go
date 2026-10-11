package repochange

import "sync"

var watcherLifetimes struct {
	sync.Mutex
	owners int
}

// AcquireWatcherLifetime keeps shared filesystem streams alive while an engine uses them.
// The final engine retires streams also started by shared inventories and caches.
func AcquireWatcherLifetime() func() {
	watcherLifetimes.Lock()
	watcherLifetimes.owners++
	watcherLifetimes.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			watcherLifetimes.Lock()
			defer watcherLifetimes.Unlock()
			watcherLifetimes.owners--
			if watcherLifetimes.owners == 0 {
				CloseWatchers()
			}
		})
	}
}
