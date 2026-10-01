// Package blobcache stores size-bounded content-addressed caches.
package blobcache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// KeepFunc reports whether a cache basename (e.g. "<hash>.<ext>") must be retained
// during size-cap eviction. Nil means nothing is pinned.
type KeepFunc func(name string) bool

// SanitizeExt normalizes a file extension to lowercase [a-z0-9], defaulting to "bin".
func SanitizeExt(ext string) string {
	ext = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ext)), ".")
	clean := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, ext)
	if clean == "" {
		return "bin"
	}
	return clean
}

// WriteFile writes (or replaces) name under dir at 0600, then Evicts.
// Used by callers that need overwrite semantics (e.g. URL-keyed fetch cache).
func WriteFile(dir, name string, data []byte, maxBytes int64, keep KeepFunc) error {
	dir = strings.TrimSpace(dir)
	name = strings.TrimSpace(name)
	if dir == "" || name == "" {
		return fmt.Errorf("blobcache: dir and name required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := replaceEntry(dir, name, data); err != nil {
		return err
	}
	Evict(dir, maxBytes, keep)
	return nil
}

// RemoveByPrefix deletes every file under dir whose basename starts with prefix+".".
func RemoveByPrefix(dir, prefix string) {
	dir = strings.TrimSpace(dir)
	prefix = strings.TrimSpace(prefix)
	if dir == "" || prefix == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix+".") {
			_ = os.Remove(filepath.Join(dir, entry.Name())) // #nosec G304 -- entry is a direct child.
		}
	}
}

// Evict removes least-recently-used entries until the directory is within maxBytes.
// Entries for which keep(basename) is true are never removed. Best-effort.
func Evict(dir string, maxBytes int64, keep KeepFunc) {
	if maxBytes <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		name string
		size int64
		mod  time.Time
	}
	var items []item
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		items = append(items, item{
			path: filepath.Join(dir, e.Name()),
			name: e.Name(),
			size: info.Size(),
			mod:  info.ModTime(),
		})
	}
	if total <= maxBytes {
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for _, it := range items {
		if total <= maxBytes {
			break
		}
		if keep != nil && keep(it.name) {
			continue
		}
		if os.Remove(it.path) == nil {
			total -= it.size
		}
	}
}

// Entry names stay beneath dir through fseffect.Location.
func replaceEntry(dir, name string, data []byte) error {
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: dir, Rel: name},
		Source:   bytes.NewReader(data),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}
