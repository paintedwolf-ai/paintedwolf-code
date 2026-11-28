//go:build !darwin || !cgo

package repochange

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Flush emits a filtered sentinel event on the shared inotify stream and waits
// until fsnotifyMux.dispatch has routed all prior kernel events into w.events.
func (w *fsnotifyPlatformWatcher) Flush(ctx context.Context) bool {
	m := w.mux
	dir, ok := m.ensureSentinel(w)
	if !ok {
		return false
	}
	m.mu.Lock()
	if w.closed || m.fw == nil {
		m.mu.Unlock()
		return false
	}
	if m.sentinels == nil {
		m.sentinels = make(map[string]chan struct{})
	}
	m.sentinelSeq++
	token := fmt.Sprintf("s-%d", m.sentinelSeq)
	barrier := make(chan struct{})
	m.sentinels[token] = barrier
	m.mu.Unlock()

	sentinelPath := filepath.Join(dir, token)
	if err := os.Mkdir(sentinelPath, 0o700); err != nil {
		m.cancelSentinel(token)
		return false
	}
	_ = os.Remove(sentinelPath)

	select {
	case <-barrier:
		return true
	case <-ctx.Done():
		m.cancelSentinel(token)
		return false
	}
}

func (m *fsnotifyMux) ensureSentinel(w *fsnotifyPlatformWatcher) (string, bool) {
	m.ops.Lock()
	defer m.ops.Unlock()
	if w.closed || m.fw == nil {
		return "", false
	}
	if m.sentinelDir != "" {
		return m.sentinelDir, true
	}
	dir, err := os.MkdirTemp("", "pw-inotify-barrier-*")
	if err != nil {
		return "", false
	}
	stream := streamPath(dir)
	if err := m.fw.Add(stream); err != nil {
		_ = os.RemoveAll(dir)
		return "", false
	}
	m.mu.Lock()
	m.sentinelDir = dir
	m.sentinelStream = stream
	m.mu.Unlock()
	return dir, true
}

func (m *fsnotifyMux) cancelSentinel(token string) {
	m.mu.Lock()
	delete(m.sentinels, token)
	m.mu.Unlock()
}

func (m *fsnotifyMux) handleSentinelEventLocked(name string) bool {
	if m.sentinelStream == "" {
		return false
	}
	clean := filepath.Clean(name)
	if clean != m.sentinelStream && !strings.HasPrefix(clean, m.sentinelStream+string(filepath.Separator)) {
		return false
	}
	if clean != m.sentinelStream {
		token := filepath.Base(clean)
		if ch, ok := m.sentinels[token]; ok {
			delete(m.sentinels, token)
			close(ch)
		}
	}
	return true
}
