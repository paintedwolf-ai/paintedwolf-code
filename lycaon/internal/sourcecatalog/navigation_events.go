package sourcecatalog

import "sync"

type navigationObservers struct {
	mu        sync.Mutex
	next      uint64
	listeners map[uint64]navigationObserver
}
type navigationObserver struct {
	root    string
	changed func()
}

// SubscribeNavigation reports committed metadata changes, without copying paths
// or holding the catalog lock while a subscriber receives the notification.
func (c *Catalog) SubscribeNavigation(root Root, changed func()) func() {
	observers := &c.navigationObservers
	observers.mu.Lock()
	if observers.listeners == nil {
		observers.listeners = make(map[uint64]navigationObserver)
	}
	observers.next++
	id := observers.next
	observers.listeners[id] = navigationObserver{root.Path, changed}
	observers.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { observers.mu.Lock(); delete(observers.listeners, id); observers.mu.Unlock() })
	}
}
func (c *Catalog) navigationChanged(root Root) {
	observers := &c.navigationObservers
	observers.mu.Lock()
	listeners := make([]func(), 0, len(observers.listeners))
	for _, observer := range observers.listeners {
		if observer.root == root.Path {
			listeners = append(listeners, observer.changed)
		}
	}
	observers.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}
