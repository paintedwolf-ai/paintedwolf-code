package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// buildSnapshot records budget-limited directories as boundaries for live listing.
func buildSnapshot(ctx context.Context, roots []Root, policy walkPolicy) (Snapshot, error) {
	snapshot := Snapshot{State: StateReady, Roots: append([]Root(nil), roots...), Entries: make([]Entry, 0, 1024)}
	for _, root := range roots {
		boundaries := make(map[string]struct{})
		opts := policy.options()
		if len(roots) > 1 {
			// A policy orders one root; several roots share only its budgets.
			opts.Scope = nil
		}
		opts.OnBoundary = func(b sandbox.SurveyBoundary) {
			boundaries[b.Rel] = struct{}{}
			slog.DebugContext(ctx, "catalog walk boundary", "root", root.Path, "path", b.Rel, "reason", string(b.Reason), "entries", b.Entries)
		}
		err := sandbox.SurveyWalk(ctx, root.Path, opts, func(item sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			if repochange.IsPrivatePath(item.Abs) {
				return sandbox.SurveySkipDir, nil
			}
			if err := nextMetadataEntry(ctx); err != nil {
				return sandbox.SurveyStop, err
			}
			info, infoErr := item.DirEntry.Info()
			if infoErr != nil {
				return sandbox.SurveyContinue, fmt.Errorf("catalog metadata %s: %w", item.Rel, infoErr)
			}
			rel := filepath.ToSlash(item.Rel)
			parent := normalizeDir(path.Dir(rel))
			entry := Entry{
				RootID: root.ID, Path: rel, Parent: parent, Name: path.Base(rel), Depth: item.Depth,
				IsDir: item.IsDir, IsSymlink: item.IsSymlink, Size: info.Size(),
				Mode: uint32(info.Mode()), Modified: info.ModTime().UTC(),
			}
			if item.IsDir {
				entry.IsVCSRoot = gitrepo.IsRoot(item.Abs)
			}
			if item.IsSymlink {
				if target, statErr := os.Stat(item.Abs); statErr == nil {
					entry.TargetIsDir = target.IsDir()
				}
			}
			snapshot.Entries = append(snapshot.Entries, entry)
			return sandbox.SurveyContinue, nil
		})
		if err != nil {
			return Snapshot{Roots: append([]Root(nil), roots...)}, fmt.Errorf("catalog root %s: %w", root.ID, err)
		}
		if len(boundaries) > 0 {
			for i := range snapshot.Entries {
				entry := &snapshot.Entries[i]
				if entry.RootID != root.ID || !entry.IsDir {
					continue
				}
				if _, bounded := boundaries[entry.Path]; bounded {
					entry.Boundary = true
				}
			}
			if _, rootBounded := boundaries["."]; rootBounded {
				snapshot.RootBoundary = true
			}
		}
	}
	snapshot.index()
	return snapshot, nil
}

func cleanRoots(roots []Root) ([]Root, error) {
	if len(roots) == 0 {
		return nil, errors.New("source catalog requires at least one root")
	}
	cleaned := make([]Root, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		id := strings.TrimSpace(root.ID)
		rootPath := cleanAbs(root.Path)
		if id == "" || rootPath == "" {
			return nil, errors.New("source catalog root id and path are required")
		}
		key := id + "\x00" + rootPath
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		cleaned = append(cleaned, Root{ID: id, Path: rootPath, Within: cleanAbs(root.Within)})
	}
	return cleaned, nil
}

func rootKey(projectID string, root Root) string {
	return strings.TrimSpace(projectID) + "\x00" + root.ID + "\x00" + root.Path
}

func combineSnapshots(roots []Root, parts []Snapshot) Snapshot {
	out := Snapshot{
		State: StateReady, Roots: append([]Root(nil), roots...),
		Entries: make([]Entry, 0), Epochs: make(map[string]repochange.Epoch),
	}
	for _, part := range parts {
		for rootID, epoch := range part.Epochs {
			out.Epochs[rootID] = epoch
		}
		if part.Revision > out.Revision {
			out.Revision = part.Revision
		}
		out.Refreshing = out.Refreshing || part.Refreshing
		out.Moving = out.Moving || part.Moving
		if part.State == StateFailed {
			out.State = StateFailed
		} else if part.State == StateWarming && out.State == StateReady {
			out.State = StateWarming
		}
		if part.Error != "" {
			if out.Error != "" {
				out.Error += "; "
			}
			out.Error += part.Error
		}
		out.Entries = append(out.Entries, part.Entries...)
	}
	out.index()
	return out
}

func entryKey(rootID, dir string) string { return rootID + "\x00" + normalizeDir(dir) }

func normalizeDir(dir string) string {
	dir = strings.TrimSpace(filepath.ToSlash(dir))
	if dir == "" || dir == "." {
		return "."
	}
	return strings.TrimPrefix(path.Clean("/"+dir), "/")
}

func underDir(candidate, dir string) bool {
	dir = normalizeDir(dir)
	return dir == "." || strings.HasPrefix(candidate, dir+"/")
}

func pathDepth(rel string) int {
	rel = normalizeDir(rel)
	if rel == "." {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

func cleanAbs(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return ""
	}
	return filepath.Clean(abs)
}
