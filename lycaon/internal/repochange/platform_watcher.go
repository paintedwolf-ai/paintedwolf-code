package repochange

import (
	"context"

	"github.com/fsnotify/fsnotify"
)

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

// SettleStatus reports the outcome of a watcher barrier and quiet wait.
type SettleStatus int

const (
	// SettleQuiet means every watched stream proved delivery up to "now" via
	// a sentinel barrier and the debouncer reached quiet.
	SettleQuiet SettleStatus = iota
	// SettleUnproven means no sentinel barrier covers every stream for the root
	// (e.g. a cross-volume root or secondary FSEvents stream). Authoritative
	// callers must verify admitted content directly rather than trusting the
	// watcher.
	SettleUnproven
	// SettleUnsettled means the barrier or quiet wait timed out or was canceled
	// before settling; authoritative callers must fail closed.
	SettleUnsettled
)

// flushingPlatformWatcher is implemented by platform watchers that can flush
// events up to "now" into EventChannel/ResyncChannel on demand.
type flushingPlatformWatcher interface {
	Flush(ctx context.Context) SettleStatus
}
