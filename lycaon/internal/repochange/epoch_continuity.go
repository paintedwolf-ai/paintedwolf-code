package repochange

import (
	"path/filepath"
	"strings"
)

// ContiguousAfter reports whether ev covers every generation after prior without a gap.
func (ev Event) ContiguousAfter(prior Epoch) bool {
	if prior.BootID == "" || ev.EpochBootID != prior.BootID || ev.WorktreeEpoch <= prior.Value {
		return false
	}
	first := ev.FirstWorktreeEpoch
	if first == 0 || first > ev.WorktreeEpoch {
		first = ev.WorktreeEpoch
	}
	return first == prior.Value+1
}

func normalizeDebouncedChanges(projectDir, key string, changes []WorktreeChange, source Source) []WorktreeChange {
	normalized := make([]WorktreeChange, 0, len(changes))
	for _, change := range changes {
		path := normalizePathForRoot(key, change.Path)
		if path == "" {
			continue
		}
		if source == SourceWatcher && path != "." && IsPrivatePath(filepath.Join(projectDir, path)) {
			continue
		}
		normalized = append(normalized, WorktreeChange{Path: path, Kind: change.Kind})
	}
	return normalized
}

func canonicalDir(root string) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	return filepath.Clean(abs)
}

func physicalDir(root string) string {
	abs := canonicalDir(root)
	if abs == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	cur := abs
	var tail []string
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return filepath.Clean(resolved)
		}
	}
}
