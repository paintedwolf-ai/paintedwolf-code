package sourcecatalog

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
)

func (c *Catalog) reconcile(ctx context.Context, projectID string, root Root) (Snapshot, error) {
	c.mu.Lock()
	rec := c.records[rootKey(projectID, root)]
	base := rec.snapshot
	full := rec.fullReconcile || base.State != StateReady
	paths := make([]string, 0, len(rec.dirtyPaths))
	for p := range rec.dirtyPaths {
		paths = append(paths, p)
	}
	rec.dirtyPaths = nil
	rec.fullReconcile = false
	c.mu.Unlock()
	policy := c.policyFor(ctx, root.Path)
	if full || !repochange.Coverage(root.watchRoot()).Complete {
		return c.build(ctx, []Root{root}, policy)
	}
	if len(paths) == 0 {
		// The watcher delivered nothing that touches this tree since base.
		base.Moving = false
		return base, nil
	}
	return reconcilePaths(ctx, root, base, paths, policy)
}

func reconcilePaths(ctx context.Context, root Root, base Snapshot, changed []string, policy walkPolicy) (Snapshot, error) {
	paths, ok := reconciliationPaths(root, changed)
	if !ok {
		return buildSnapshot(ctx, []Root{root}, policy)
	}
	entries := make(map[string]Entry, len(base.Entries))
	for _, entry := range base.Entries {
		entries[entry.Path] = entry
	}
	reconciled := make(map[string]bool, len(paths))
	for _, rel := range paths {
		if err := nextMetadataEntry(ctx); err != nil {
			return Snapshot{}, err
		}
		if sandbox.ShouldSkipDir(rel, path.Base(rel)) || repochange.IsPrivatePath(filepath.Join(root.Path, filepath.FromSlash(rel))) {
			continue
		}
		// Ancestor checks reject replacements before descendant access.
		parts := strings.Split(rel, "/")
		for depth := 1; depth < len(parts); depth++ {
			parent := strings.Join(parts[:depth], "/")
			info, err := os.Lstat(filepath.Join(root.Path, filepath.FromSlash(parent)))
			if errors.Is(err, os.ErrNotExist) || (err == nil && !info.IsDir()) {
				rel = parent
				break
			}
			if err != nil {
				return Snapshot{}, err
			}
			entries[parent] = catalogEntry(root.ID, parent, info, root.Path)
		}
		covered := false
		for parent := rel; parent != "."; parent = path.Dir(parent) {
			if reconciled[parent] {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		if base.underBoundary(root.ID, rel) {
			// Unobserved descendants cannot change this generation.
			continue
		}
		reconciled[rel] = true
		removeCatalogSubtree(entries, base, root.ID, rel)
		abs := filepath.Join(root.Path, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Snapshot{}, err
		}
		if sandbox.ShouldSkipDir(rel, path.Base(rel)) {
			continue
		}
		entries[rel] = catalogEntry(root.ID, rel, info, root.Path)
		if !info.IsDir() {
			continue
		}
		subtree, err := buildSnapshot(ctx, []Root{{ID: root.ID, Path: abs}}, policy.under(rel))
		if err != nil {
			return Snapshot{}, err
		}
		if subtree.RootBoundary {
			bounded := entries[rel]
			bounded.Boundary = true
			entries[rel] = bounded
		}
		for _, entry := range subtree.Entries {
			entry.Path = path.Join(rel, entry.Path)
			entry.Parent = normalizeDir(path.Dir(entry.Path))
			entry.Depth = pathDepth(entry.Path)
			entries[entry.Path] = entry
		}
	}
	return snapshotFromEntries(root, entries), nil
}

func removeCatalogSubtree(entries map[string]Entry, base Snapshot, rootID, rel string) {
	pending := []string{rel}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		delete(entries, current)
		for _, index := range base.children[entryKey(rootID, current)] {
			pending = append(pending, base.Entries[index].Path)
		}
	}
}

func reconciliationPaths(root Root, changed []string) ([]string, bool) {
	paths := make([]string, 0, len(changed))
	for _, p := range changed {
		if filepath.IsAbs(p) {
			var err error
			p, err = filepath.Rel(root.Path, p)
			if err != nil {
				return nil, false
			}
		}
		p = filepath.ToSlash(filepath.Clean(p))
		if p == "." || p == ".." || strings.HasPrefix(p, "../") {
			return nil, false
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := paths[:0]
	for _, p := range paths {
		if len(out) > 0 && (p == out[len(out)-1] || strings.HasPrefix(p, out[len(out)-1]+"/")) {
			continue
		}
		out = append(out, p)
	}
	return out, true
}

func catalogEntry(rootID, rel string, info os.FileInfo, rootPath string) Entry {
	e := Entry{RootID: rootID, Path: rel, Parent: normalizeDir(path.Dir(rel)), Name: path.Base(rel),
		Depth: pathDepth(rel), IsDir: info.IsDir(), IsSymlink: info.Mode()&os.ModeSymlink != 0,
		Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime().UTC()}
	abs := filepath.Join(rootPath, filepath.FromSlash(rel))
	if e.IsDir {
		e.IsVCSRoot = gitrepo.IsRoot(abs)
	}
	if e.IsSymlink {
		if target, err := os.Stat(abs); err == nil {
			e.TargetIsDir = target.IsDir()
		}
	}
	return e
}

func snapshotFromEntries(root Root, entries map[string]Entry) Snapshot {
	ordered := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	return NewSnapshot([]Root{root}, ordered)
}
