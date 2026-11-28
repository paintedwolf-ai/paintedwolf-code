//go:build darwin && cgo

package repochange

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/fsnotify/fsevents"
)

// setupFSEventsSentinel creates a private temp directory outside the user's
// tree on the same volume as rootResolved so sentinel events share the exact
// kernel /dev/fsevents FIFO stream as rootResolved.
func setupFSEventsSentinel(rootResolved string) (string, string) {
	dir, err := os.MkdirTemp("", "pw-fsevents-barrier-*")
	if err != nil {
		return "", ""
	}
	resolved, err := resolveWatchPath(dir)
	if err != nil || !sameFilesystemDevice(rootResolved, resolved) {
		_ = os.RemoveAll(dir)
		return "", ""
	}
	return dir, resolved
}

func sameFilesystemDevice(a, b string) bool {
	var sa, sb syscall.Stat_t
	if err := syscall.Stat(a, &sa); err != nil {
		return false
	}
	if err := syscall.Stat(b, &sb); err != nil {
		return false
	}
	return sa.Dev == sb.Dev
}

// Flush waits until macOS fseventsd has delivered all events up to "now" into
// EventChannel/ResyncChannel. FSEventStreamFlushSync alone only flushes events
// fseventsd has already ingested from /dev/fsevents, so we emit a filtered
// sentinel event in our private same-volume temp dir on the same FSEventStream
// and wait for it to pass through pump and deliver in FIFO order.
func (w *fseventsPlatformWatcher) Flush(ctx context.Context) bool {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return false
	}
	pumps := slices.Clone(w.pumps)
	w.mu.Unlock()

	if w.sentinelResolved != "" && len(pumps) > 0 {
		if !w.flushViaSentinel(ctx, pumps) {
			return false
		}
		if len(pumps) > 1 {
			return w.flushStreamsDirect(ctx, pumps[1:])
		}
		return true
	}
	return w.flushStreamsDirect(ctx, pumps)
}

func (w *fseventsPlatformWatcher) flushViaSentinel(ctx context.Context, pumps []fseventsStreamPump) bool {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return false
	}
	w.sentinelSeq++
	token := fmt.Sprintf("s-%d", w.sentinelSeq)
	barrier := make(chan struct{})
	w.sentinels[token] = barrier
	w.mu.Unlock()

	sentinelPath := filepath.Join(w.sentinelDir, token)
	if err := os.WriteFile(sentinelPath, nil, 0o600); err != nil {
		w.cancelSentinel(token)
		return false
	}
	_ = os.Remove(sentinelPath)

	// Non-blocking flush nudges fseventsd without waiting for fseventsLatency.
	pumps[0].stream.Flush(false)

	select {
	case <-barrier:
		return true
	case <-w.done:
		w.cancelSentinel(token)
		return false
	case <-ctx.Done():
		w.cancelSentinel(token)
		return false
	}
}

func (w *fseventsPlatformWatcher) cancelSentinel(token string) {
	w.mu.Lock()
	delete(w.sentinels, token)
	w.mu.Unlock()
}

func (w *fseventsPlatformWatcher) flushStreamsDirect(ctx context.Context, pumps []fseventsStreamPump) bool {
	for _, sp := range pumps {
		flushed := make(chan struct{})
		go func(s *fsevents.EventStream) {
			s.Flush(true)
			close(flushed)
		}(sp.stream)
		select {
		case <-flushed:
		case <-w.done:
			return false
		case <-ctx.Done():
			return false
		}
		ack := make(chan struct{})
		select {
		case sp.sync <- ack:
		case <-w.done:
			return false
		case <-ctx.Done():
			return false
		}
		select {
		case <-ack:
		case <-w.done:
			return false
		case <-ctx.Done():
			return false
		}
	}
	barrier := make(chan struct{})
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return false
	}
	w.barriers = append(w.barriers, barrier)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
	select {
	case <-barrier:
		return true
	case <-w.done:
		return false
	case <-ctx.Done():
		return false
	}
}

func (w *fseventsPlatformWatcher) drainStream(stream *fsevents.EventStream) bool {
	for {
		select {
		case batch, ok := <-stream.Events:
			if !ok {
				return false
			}
			w.enqueueBatch(batch)
		default:
			return true
		}
	}
}

func (w *fseventsPlatformWatcher) enqueueBatch(batch []fsevents.Event) {
	var matched []string
	sawResync := false
	for _, event := range batch {
		if token, isSentinel := w.matchSentinelPath(event.Path); isSentinel {
			if token != "" {
				matched = append(matched, token)
			}
			if event.Flags&(fsevents.MustScanSubDirs|fsevents.KernelDropped|fsevents.UserDropped|fsevents.EventIDsWrapped|fsevents.RootChanged) != 0 {
				sawResync = true
			}
			continue
		}
		if translated := w.translate(event); translated.resync || translated.event.Op != 0 {
			if translated.resync {
				sawResync = true
			}
			w.enqueue(translated)
		}
	}
	if sawResync || len(matched) > 0 {
		w.releaseSentinels(matched, sawResync)
	}
}

// matchSentinelPath filters all events under the private sentinel directory so
// barrier writes never surface as user worktree changes.
func (w *fseventsPlatformWatcher) matchSentinelPath(raw string) (string, bool) {
	if w.sentinelResolved == "" {
		return "", false
	}
	physical := filepath.Clean(raw)
	rel, ok := relativeToRoot(w.sentinelResolved, physical)
	if !ok {
		return "", false
	}
	if rel == "." || strings.Contains(rel, string(filepath.Separator)) {
		return "", true
	}
	return rel, true
}

func (w *fseventsPlatformWatcher) releaseSentinels(tokens []string, all bool) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	released := false
	if all {
		for token, barrier := range w.sentinels {
			delete(w.sentinels, token)
			w.barriers = append(w.barriers, barrier)
			released = true
		}
	} else {
		for _, token := range tokens {
			if barrier, ok := w.sentinels[token]; ok {
				delete(w.sentinels, token)
				w.barriers = append(w.barriers, barrier)
				released = true
			}
		}
	}
	w.mu.Unlock()
	if released {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}
