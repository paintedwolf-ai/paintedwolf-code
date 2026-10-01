package repochange

import "github.com/fsnotify/fsnotify"

// platformWatcher is the fsnotify surface the registry needs, so macOS can use
// recursive FSEvents instead of kqueue's descriptor-per-entry watches.
type platformWatcher interface {
	Add(string) error
	Remove(string) error
	Close() error
	WatchList() []string
	EventChannel() <-chan fsnotify.Event
	ResyncChannel() <-chan struct{}
	ErrorChannel() <-chan error
}
