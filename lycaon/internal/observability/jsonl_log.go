package observability

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/debugretention"
)

type jsonlDebugLog struct {
	path string
	mu   sync.Mutex
	f    *debugretention.File
}

func openJSONLDebugLog(enabled bool, kind debugpaths.Kind) (*jsonlDebugLog, error) {
	if !enabled {
		return nil, nil
	}
	return openJSONLDebugLogAt(debugpaths.FilePath(kind))
}

// openJSONLDebugLogAt opens a sibling capture log.
func openJSONLDebugLogAt(path string) (*jsonlDebugLog, error) {
	f, err := debugretention.OpenFile(path, debugretention.DefaultConfig().MaxFileBytes)
	if err != nil {
		return nil, fmt.Errorf("open debug log: %w", err)
	}
	return &jsonlDebugLog{path: path, f: f}, nil
}

func (l *jsonlDebugLog) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

func (l *jsonlDebugLog) write(entry any) {
	if l == nil {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("debug log marshal failed", "path", l.path, "err", err)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	if _, err := l.f.Write(append(data, '\n')); err != nil {
		slog.Warn("debug log write failed", "path", l.path, "err", err)
	}
}
