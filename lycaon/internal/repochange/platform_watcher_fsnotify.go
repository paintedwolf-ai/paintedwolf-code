//go:build !darwin || !cgo

package repochange

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// rootEventBuffer bounds how far one root may lag the shared stream before its
// dropped events become an overflow resync instead of stalling other roots.
const rootEventBuffer = 1024

// rootErrorBuffer holds pending stream errors; every one resyncs the root, so a
// full buffer already guarantees the root will rescan.
const rootErrorBuffer = 4

// sharedFsnotify is the process's one fsnotify stream. inotify caps instances
// per user (fs.inotify.max_user_instances, commonly 128) across every process,
// while watchfd already budgets the registrations themselves, so a root costs
// registrations rather than an instance.
var sharedFsnotify = &fsnotifyMux{}

type fsnotifyMux struct {
	// ops serializes stream registration and lifetime. It is never held by
	// dispatch, which fsnotify may wait on while holding its own lock.
	ops sync.Mutex
	// mu guards routing; dispatch takes only mu.
	mu    sync.Mutex
	fw    *fsnotify.Watcher
	roots map[*fsnotifyPlatformWatcher]struct{}
	// routes maps each path the stream reports to the roots registered on it
	// and the spelling each root registered.
	routes map[string]map[*fsnotifyPlatformWatcher]string
}

// fsnotifyPlatformWatcher is one root's view of the shared stream. Event names
// use the root's own spelling of each registered directory.
type fsnotifyPlatformWatcher struct {
	mux    *fsnotifyMux
	events chan fsnotify.Event
	errors chan error
	resync chan struct{}
	// streamPaths maps each registered path to the path the stream reports
	// for it; guarded by mux.mu.
	streamPaths map[string]string
	closed      bool
}

func newPlatformWatcher(string) (platformWatcher, error) { return sharedFsnotify.subscribe() }

func (m *fsnotifyMux) subscribe() (*fsnotifyPlatformWatcher, error) {
	m.ops.Lock()
	defer m.ops.Unlock()
	if m.fw == nil {
		fw, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.fw = fw
		m.roots = map[*fsnotifyPlatformWatcher]struct{}{}
		m.routes = map[string]map[*fsnotifyPlatformWatcher]string{}
		m.mu.Unlock()
		go m.dispatch(fw)
	}
	w := &fsnotifyPlatformWatcher{
		mux:         m,
		events:      make(chan fsnotify.Event, rootEventBuffer),
		errors:      make(chan error, rootErrorBuffer),
		resync:      make(chan struct{}),
		streamPaths: map[string]string{},
	}
	m.mu.Lock()
	m.roots[w] = struct{}{}
	m.mu.Unlock()
	return w, nil
}

// streamPath is the spelling the stream reports for a directory. inotify names
// events after the first path that registered an inode, so registering the
// resolved path keeps one spelling per directory whichever root aliases it.
func streamPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return resolved
}

func (w *fsnotifyPlatformWatcher) Add(path string) error {
	m := w.mux
	m.ops.Lock()
	defer m.ops.Unlock()
	if w.closed {
		return fsnotify.ErrClosed
	}
	stream := streamPath(path)
	if err := m.fw.Add(stream); err != nil {
		return err
	}
	m.mu.Lock()
	// A path whose link now resolves elsewhere moves to the new target.
	previous, moved := w.streamPaths[path]
	moved = moved && previous != stream
	released := moved && m.unrouteLocked(w, path, previous)
	w.streamPaths[path] = stream
	if m.routes[stream] == nil {
		m.routes[stream] = map[*fsnotifyPlatformWatcher]string{}
	}
	m.routes[stream][w] = path
	m.mu.Unlock()
	if released {
		_ = m.fw.Remove(previous)
	}
	return nil
}

func (w *fsnotifyPlatformWatcher) Remove(path string) error {
	m := w.mux
	m.ops.Lock()
	defer m.ops.Unlock()
	m.mu.Lock()
	stream, ok := w.streamPaths[path]
	if !ok {
		m.mu.Unlock()
		return fsnotify.ErrNonExistentWatch
	}
	delete(w.streamPaths, path)
	released := m.unrouteLocked(w, path, stream)
	m.mu.Unlock()
	if !released {
		return nil
	}
	return m.fw.Remove(stream)
}

// unrouteLocked drops one root's route and reports whether no root still
// listens on the stream path.
func (m *fsnotifyMux) unrouteLocked(w *fsnotifyPlatformWatcher, path, stream string) bool {
	listeners := m.routes[stream]
	if listeners[w] == path {
		delete(listeners, w)
	}
	if len(listeners) > 0 {
		return false
	}
	delete(m.routes, stream)
	return true
}

// Close releases the root's registrations; the last root closes the stream.
func (w *fsnotifyPlatformWatcher) Close() error {
	m := w.mux
	m.ops.Lock()
	defer m.ops.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	m.mu.Lock()
	var released []string
	for path, stream := range w.streamPaths {
		if m.unrouteLocked(w, path, stream) {
			released = append(released, stream)
		}
	}
	w.streamPaths = nil
	delete(m.roots, w)
	fw := m.fw
	last := len(m.roots) == 0
	if last {
		m.fw, m.roots, m.routes = nil, nil, nil
	}
	m.mu.Unlock()
	if last {
		return fw.Close()
	}
	var errs []error
	for _, stream := range released {
		// The kernel drops a watch when its directory is deleted.
		if err := fw.Remove(stream); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *fsnotifyPlatformWatcher) WatchList() []string {
	w.mux.mu.Lock()
	defer w.mux.mu.Unlock()
	out := make([]string, 0, len(w.streamPaths))
	for path := range w.streamPaths {
		out = append(out, path)
	}
	return out
}

func (w *fsnotifyPlatformWatcher) EventChannel() <-chan fsnotify.Event { return w.events }
func (w *fsnotifyPlatformWatcher) ResyncChannel() <-chan struct{}      { return w.resync }
func (w *fsnotifyPlatformWatcher) ErrorChannel() <-chan error          { return w.errors }

// dispatch routes one stream until it closes. It never blocks on a root: a
// root that cannot keep up loses events and receives an overflow, which its
// loop closes with a resync exactly as it would a kernel queue overflow.
func (m *fsnotifyMux) dispatch(fw *fsnotify.Watcher) {
	events, errs := fw.Events, fw.Errors
	for events != nil || errs != nil {
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			m.route(ev)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			m.broadcast(err)
		}
	}
}

// route delivers an event to every root registered on the event's directory or
// on the named path itself, renamed into that root's spelling.
func (m *fsnotifyMux) route(ev fsnotify.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delivered := map[*fsnotifyPlatformWatcher]struct{}{}
	for _, stream := range []string{filepath.Dir(ev.Name), ev.Name} {
		for w, path := range m.routes[stream] {
			if _, ok := delivered[w]; ok {
				continue
			}
			delivered[w] = struct{}{}
			renamed := ev
			renamed.Name = path + strings.TrimPrefix(ev.Name, stream)
			select {
			case w.events <- renamed:
			default:
				w.sendError(fsnotify.ErrEventOverflow)
			}
		}
	}
}

// broadcast reports a stream error to every root: a dropped kernel queue or a
// failed stream may have hidden any root's writes.
func (m *fsnotifyMux) broadcast(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for w := range m.roots {
		w.sendError(err)
	}
}

func (w *fsnotifyPlatformWatcher) sendError(err error) {
	select {
	case w.errors <- err:
	default:
	}
}
