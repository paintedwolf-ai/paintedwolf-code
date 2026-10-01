//go:build darwin && cgo

package repochange

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsevents"
	"github.com/fsnotify/fsnotify"
)

const (
	fseventsLatency    = 25 * time.Millisecond
	fseventsQueueLimit = 4096
)

type fseventsWatchRoot struct {
	logical  string
	resolved string
}

type fseventsDelivery struct {
	event  fsnotify.Event
	resync bool
}

// Native streams cover roots recursively and add external metadata trees.
type fseventsPlatformWatcher struct {
	root   string
	events chan fsnotify.Event
	resync chan struct{}
	errors chan error
	done   chan struct{}
	wake   chan struct{}
	wg     sync.WaitGroup
	once   sync.Once
	addMu  sync.Mutex

	mu        sync.Mutex
	streams   []*fsevents.EventStream
	watched   map[string]struct{}
	roots     []fseventsWatchRoot
	queue     []fseventsDelivery
	collapsed bool
	closed    bool
}

func newPlatformWatcher(root string) (platformWatcher, error) {
	root = filepath.Clean(root)
	resolved, err := resolveWatchPath(root)
	if err != nil {
		return nil, fmt.Errorf("resolve FSEvents root: %w", err)
	}
	w := &fseventsPlatformWatcher{
		root:   root,
		events: make(chan fsnotify.Event), resync: make(chan struct{}), errors: make(chan error, 1),
		done: make(chan struct{}), wake: make(chan struct{}, 1), watched: make(map[string]struct{}),
	}
	stream, err := startFSEventStream(resolved)
	if err != nil {
		return nil, fmt.Errorf("start FSEvents stream: %w", err)
	}
	w.streams = []*fsevents.EventStream{stream}
	w.roots = []fseventsWatchRoot{{logical: root, resolved: resolved}}
	w.wg.Add(2)
	go w.pump(stream)
	go w.deliver()
	return w, nil
}

func startFSEventStream(path string) (*fsevents.EventStream, error) {
	stream := &fsevents.EventStream{
		Paths: []string{path}, Events: make(chan []fsevents.Event, 128),
		Flags:   fsevents.FileEvents | fsevents.NoDefer | fsevents.WatchRoot,
		Latency: fseventsLatency, Resume: true, EventID: fsevents.LatestEventID(),
	}
	if err := stream.Start(); err != nil {
		return nil, err
	}
	// Flush registration-time journal events before exposing the stream.
	time.Sleep(fseventsLatency)
	stream.Flush(true)
	for {
		select {
		case <-stream.Events:
			continue
		default:
			return stream, nil
		}
	}
}

func (w *fseventsPlatformWatcher) Add(path string) error {
	w.addMu.Lock()
	defer w.addMu.Unlock()
	path = filepath.Clean(path)
	resolved, err := resolveWatchPath(path)
	if err != nil {
		return fmt.Errorf("resolve FSEvents watch path: %w", err)
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return fmt.Errorf("FSEvents watcher is closed")
	}
	if _, ok := w.watched[path]; ok {
		w.mu.Unlock()
		return nil
	}
	if coveredByWatchRoot(w.roots, resolved) {
		w.watched[path] = struct{}{}
		w.roots = appendWatchAlias(w.roots, fseventsWatchRoot{logical: path, resolved: resolved})
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()

	stream, err := startFSEventStream(resolved)
	if err != nil {
		return fmt.Errorf("extend FSEvents stream: %w", err)
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		stream.Stop()
		return fmt.Errorf("FSEvents watcher is closed")
	}
	w.watched[path] = struct{}{}
	w.roots = append(w.roots, fseventsWatchRoot{logical: path, resolved: resolved})
	w.streams = append(w.streams, stream)
	w.wg.Add(1)
	w.mu.Unlock()
	go w.pump(stream)
	return nil
}

func (w *fseventsPlatformWatcher) Remove(path string) error {
	w.mu.Lock()
	delete(w.watched, filepath.Clean(path))
	w.mu.Unlock()
	return nil
}

func (w *fseventsPlatformWatcher) WatchList() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.watched))
	for path := range w.watched {
		out = append(out, path)
	}
	slices.Sort(out)
	return out
}

func (w *fseventsPlatformWatcher) EventChannel() <-chan fsnotify.Event { return w.events }
func (w *fseventsPlatformWatcher) ResyncChannel() <-chan struct{}      { return w.resync }
func (w *fseventsPlatformWatcher) ErrorChannel() <-chan error          { return w.errors }

func (w *fseventsPlatformWatcher) Close() error {
	w.once.Do(func() {
		w.addMu.Lock()
		w.mu.Lock()
		w.closed = true
		streams := slices.Clone(w.streams)
		w.mu.Unlock()
		for _, stream := range streams {
			stream.Stop()
		}
		close(w.done)
		w.addMu.Unlock()
		w.wg.Wait()
		close(w.events)
		close(w.resync)
		close(w.errors)
	})
	return nil
}

func (w *fseventsPlatformWatcher) pump(stream *fsevents.EventStream) {
	defer w.wg.Done()
	for {
		select {
		case <-w.done:
			return
		case batch, ok := <-stream.Events:
			if !ok {
				return
			}
			for _, event := range batch {
				if translated := w.translate(event); translated.resync || translated.event.Op != 0 {
					w.enqueue(translated)
				}
			}
		}
	}
}

func (w *fseventsPlatformWatcher) enqueue(delivery fseventsDelivery) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	if len(w.queue) >= fseventsQueueLimit {
		w.queue = w.queue[:0]
		w.collapsed = true
	}
	if !w.collapsed {
		w.queue = append(w.queue, delivery)
	}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *fseventsPlatformWatcher) deliver() {
	defer w.wg.Done()
	for {
		delivery, ok := w.nextDelivery()
		if !ok {
			select {
			case <-w.done:
				return
			case <-w.wake:
				continue
			}
		}
		if delivery.resync {
			select {
			case w.resync <- struct{}{}:
			case <-w.done:
				return
			}
		} else {
			select {
			case w.events <- delivery.event:
			case <-w.done:
				return
			}
		}
	}
}

func (w *fseventsPlatformWatcher) nextDelivery() (fseventsDelivery, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.collapsed {
		w.collapsed = false
		return fseventsDelivery{resync: true}, true
	}
	if len(w.queue) == 0 {
		return fseventsDelivery{}, false
	}
	delivery := w.queue[0]
	w.queue = w.queue[1:]
	return delivery, true
}

func (w *fseventsPlatformWatcher) translate(event fsevents.Event) fseventsDelivery {
	w.mu.Lock()
	roots := slices.Clone(w.roots)
	w.mu.Unlock()
	physical := filepath.Clean(event.Path)
	name := physical
	best := -1
	for _, root := range roots {
		if _, ok := relativeToRoot(root.resolved, physical); ok && len(root.resolved) > best {
			best = len(root.resolved)
			if rel, _ := filepath.Rel(root.resolved, physical); rel == "." {
				name = root.logical
			} else {
				name = filepath.Join(root.logical, rel)
			}
		}
	}
	flags := event.Flags
	if flags&(fsevents.MustScanSubDirs|fsevents.KernelDropped|fsevents.UserDropped|fsevents.EventIDsWrapped|fsevents.RootChanged) != 0 {
		return fseventsDelivery{resync: true}
	}
	var op fsnotify.Op
	if flags&fsevents.ItemCreated != 0 {
		op |= fsnotify.Create
	}
	if flags&fsevents.ItemRemoved != 0 {
		op |= fsnotify.Remove
	}
	if flags&fsevents.ItemRenamed != 0 {
		op |= fsnotify.Rename
	}
	if flags&fsevents.ItemModified != 0 {
		op |= fsnotify.Write
	}
	if flags&(fsevents.ItemInodeMetaMod|fsevents.ItemChangeOwner) != 0 {
		op |= fsnotify.Chmod
	}
	return fseventsDelivery{event: fsnotify.Event{Name: name, Op: op}}
}

func resolveWatchPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func coveredByWatchRoot(roots []fseventsWatchRoot, path string) bool {
	for _, root := range roots {
		if _, ok := relativeToRoot(root.resolved, path); ok {
			return true
		}
	}
	return false
}

func appendWatchAlias(roots []fseventsWatchRoot, candidate fseventsWatchRoot) []fseventsWatchRoot {
	for _, root := range roots {
		if root == candidate {
			return roots
		}
	}
	return append(roots, candidate)
}

func relativeToRoot(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	return rel, err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
