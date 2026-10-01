package debugretention

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

type captureSession struct {
	path     string
	modified time.Time
	bytes    int64
	protect  bool
}

// Run prunes closed debug capture sessions immediately and on the configured
// cadence until ctx is canceled.
func Run(ctx context.Context, dataDir string, cfg Config) error {
	cfg = cfg.normalized()
	if err := PruneSessions(ctx, dataDir, cfg); err != nil {
		return err
	}
	ticker := time.NewTicker(cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := PruneSessions(ctx, dataDir, cfg); err != nil {
				return err
			}
		}
	}
}

// PruneSessions enforces both the session-count and aggregate-byte bounds.
func PruneSessions(ctx context.Context, dataDir string, cfg Config) error {
	cfg = cfg.normalized()
	if strings.TrimSpace(dataDir) == "" {
		return fmt.Errorf("debug retention: data dir required")
	}
	root := filepath.Join(debugpaths.DebugRootUnder(dataDir), "sessions")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("debug retention: read sessions: %w", err)
	}
	protected := protectedSessionDirs(root)
	sessions := make([]captureSession, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		bytes, modified, err := treeStats(path)
		if err != nil {
			return fmt.Errorf("debug retention: inspect %s: %w", path, err)
		}
		sessions = append(sessions, captureSession{
			path: path, modified: modified, bytes: bytes,
			protect: protected[filepath.Clean(path)],
		})
		total += bytes
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].modified.Before(sessions[j].modified)
	})
	remaining := len(sessions)
	for i := range sessions {
		session := &sessions[i]
		if session.protect {
			continue
		}
		if remaining <= cfg.MaxSessions && total <= cfg.MaxTotalBytes {
			break
		}
		if err := os.RemoveAll(session.path); err != nil {
			return fmt.Errorf("debug retention: remove %s: %w", session.path, err)
		}
		remaining--
		total -= session.bytes
	}
	return nil
}

func protectedSessionDirs(root string) map[string]bool {
	out := make(map[string]bool)
	for _, file := range debugpaths.Files() {
		markSessionParent(out, root, debugpaths.FilePath(file.Kind))
	}
	if target, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(root), "latest")); err == nil {
		markSessionParent(out, root, target)
	}
	return out
}

func markSessionParent(out map[string]bool, root, path string) {
	rel, err := filepath.Rel(root, filepath.Clean(strings.TrimSpace(path)))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	name := strings.SplitN(rel, string(filepath.Separator), 2)[0]
	if name != "" {
		out[filepath.Join(root, name)] = true
	}
}

func treeStats(root string) (int64, time.Time, error) {
	var bytes int64
	var modified time.Time
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			bytes += info.Size()
			if info.ModTime().After(modified) {
				modified = info.ModTime()
			}
		}
		return nil
	})
	return bytes, modified, err
}
