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
// All roots on Linux share one kernel inotify fd; if the sentinel dir cannot be
// established, Flush returns SettleUnproven so authoritative callers fall back
// to direct content verification.
func (w *fsnotifyPlatformWatcher) Flush(ctx context.Context) SettleStatus {
	m := w.mux
	dir, status := m.ensureSentinel(w)
	if status != SettleQuiet {
		return status
	}
	m.mu.Lock()
	if w.closed || m.fw == nil {
		m.mu.Unlock()
		return SettleUnsettled
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
		return SettleUnproven
	}
	_ = os.Remove(sentinelPath)

	select {
	case <-barrier:
		return SettleQuiet
	case <-ctx.Done():
		m.cancelSentinel(token)
		return SettleUnsettled
	}
}

func (m *fsnotifyMux) ensureSentinel(w *fsnotifyPlatformWatcher) (string, SettleStatus) {
	m.ops.Lock()
	defer m.ops.Unlock()
	if w.closed || m.fw == nil {
		return "", SettleUnsettled
	}
	if !checkSentinelDevice(m.sentinelDir, m.sentinelDir, nil) {
		return "", SettleUnproven
	}
	if m.sentinelDir != "" {
		return m.sentinelDir, SettleQuiet
	}
	dir, err := os.MkdirTemp("", "pw-inotify-barrier-*")
	if err != nil {
		return "", SettleUnproven
	}
	stream := streamPath(dir)
	if err := m.fw.Add(stream); err != nil {
		_ = os.RemoveAll(dir)
		return "", SettleUnproven
	}
	m.mu.Lock()
	m.sentinelDir = dir
	m.sentinelStream = stream
	m.mu.Unlock()
	return dir, SettleQuiet
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
