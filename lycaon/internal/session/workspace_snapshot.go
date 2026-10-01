package session

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/oswalk"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// WorkspaceFileFingerprint records one baseline file.
type WorkspaceFileFingerprint struct {
	Size      int64 `json:"size"`
	MtimeNano int64 `json:"mtime_ns"`
}

// SnapshotWorkspace fingerprints a project tree.
func SnapshotWorkspace(projectDir string) (map[string]WorkspaceFileFingerprint, error) {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, nil
	}
	out := make(map[string]WorkspaceFileFingerprint)
	if err := walkSnapshotDir(projectDir, out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// DiffWorkspaceSnapshot returns repo-relative paths that changed since baseline.
func DiffWorkspaceSnapshot(projectDir string, baseline map[string]WorkspaceFileFingerprint) []string {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" || baseline == nil {
		return nil
	}
	current, err := SnapshotWorkspace(projectDir)
	if err != nil {
		return nil
	}
	return diffWorkspaceSnapshots(baseline, current)
}

func diffWorkspaceSnapshots(baseline, current map[string]WorkspaceFileFingerprint) []string {
	seen := map[string]struct{}{}
	var changed []string
	add := func(rel string) {
		if _, dup := seen[rel]; dup {
			return
		}
		seen[rel] = struct{}{}
		changed = append(changed, rel)
	}
	for rel, before := range baseline {
		after, ok := current[rel]
		if ok && after.Size == before.Size && after.MtimeNano == before.MtimeNano {
			continue
		}
		add(rel)
	}
	for rel := range current {
		if _, ok := baseline[rel]; ok {
			continue
		}
		add(rel)
	}
	sort.Strings(changed)
	return changed
}

func walkSnapshotDir(projectDir string, out map[string]WorkspaceFileFingerprint) error {
	return filepath.WalkDir(projectDir, func(walked string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return oswalk.Skip(walkErr)
		}
		if walked == projectDir {
			return nil
		}
		rel, err := filepath.Rel(projectDir, walked)
		if err != nil {
			return oswalk.Skip(err)
		}
		relSlash := filepath.ToSlash(rel)
		if d.IsDir() {
			if sandbox.ShouldSkipDir(relSlash, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return oswalk.Skip(err)
		}
		recordFingerprint(projectDir, out, relSlash, info)
		return nil
	})
}
func recordFingerprint(projectDir string, out map[string]WorkspaceFileFingerprint, rel string, info fs.FileInfo) {
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	if rel == "" || info == nil || info.IsDir() {
		return
	}
	fp := WorkspaceFileFingerprint{
		Size:      info.Size(),
		MtimeNano: info.ModTime().UTC().UnixNano(),
	}

	out[rel] = fp
}
