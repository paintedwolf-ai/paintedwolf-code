// Package indexwatch notices Git index changes a command made while its
// sandbox kept the matching worktree files unchanged.
//
// Git updates the index even when the worktree write is refused, and can still
// exit 0. The capture is a copy of the index file; the comparison runs only
// when a rule reads one of its facts.
package indexwatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

// Snapshot is the index of the repository holding a root when a command started.
// The zero Snapshot observes nothing. Copies share one capture.
type Snapshot struct {
	*capture
}

type capture struct {
	root      string
	repoRoot  string
	indexPath string
	before    os.FileInfo
	copyDir   string
}

// Result lists root-relative paths the command left behind the index.
type Result struct {
	// Stale paths have a new index entry while the worktree kept the content it
	// had before the command; they were clean before it.
	Stale []string
	// Leftover paths lost their index entry while the worktree kept the file.
	Leftover []string
	// Conflicted paths gained unmerged index stages without conflict markers.
	Conflicted []string
}

// Take copies the index of the repository containing root. No repository, no
// index, or an unreadable one yields the zero Snapshot.
func Take(root string) Snapshot {
	root = strings.TrimSpace(root)
	repo, ok := gitrepo.Discover(root)
	if !ok || root == "" {
		return Snapshot{}
	}
	indexPath := filepath.Join(repo.GitDir, "index")
	before, err := os.Stat(indexPath)
	if err != nil || !before.Mode().IsRegular() {
		return Snapshot{}
	}
	dir, err := os.MkdirTemp("", "index-watch-*")
	if err != nil {
		return Snapshot{}
	}
	if err := copyIndex(indexPath, filepath.Join(dir, "index")); err != nil {
		_ = os.RemoveAll(dir)
		return Snapshot{}
	}
	// Discover canonicalizes the repository root, so the watch root has to fold
	// its symlinks too for results to stay root-relative.
	c := &capture{
		root: gitrepo.CanonicalDir(root), repoRoot: repo.Root,
		indexPath: indexPath, before: before, copyDir: dir,
	}
	// A capture whose owner drops it unreleased still leaves no copy behind.
	runtime.AddCleanup(c, func(dir string) { _ = os.RemoveAll(dir) }, dir)
	return Snapshot{c}
}

// Active reports whether a capture exists to compare against.
func (s Snapshot) Active() bool { return s.capture != nil && s.copyDir != "" }

// Root is the directory results are relative to.
func (s Snapshot) Root() string {
	if s.capture == nil {
		return ""
	}
	return s.root
}

// Release removes the host-owned index copy; later comparisons observe nothing.
func (s Snapshot) Release() {
	if !s.Active() {
		return
	}
	_ = os.RemoveAll(s.copyDir)
	s.copyDir = ""
}

// changed reports whether the live index is no longer the captured file.
func (s Snapshot) changed() bool {
	if !s.Active() {
		return false
	}
	now, err := os.Stat(s.indexPath)
	if err != nil {
		return true
	}
	return !os.SameFile(s.before, now) || now.Size() != s.before.Size() || !now.ModTime().Equal(s.before.ModTime())
}

// Stale compares the captured and live index for the paths keep accepts,
// given absolute paths. The sandbox kept those worktree files from changing,
// so the worktree still shows the content the captured index described.
func (s Snapshot) Stale(ctx context.Context, keep func(abs string) bool) (Result, error) {
	var out Result
	if !s.changed() {
		return out, nil
	}
	before, err := s.entries(ctx, filepath.Join(s.copyDir, "index"))
	if err != nil {
		return out, err
	}
	after, err := s.entries(ctx, "")
	if err != nil {
		return out, err
	}
	changed := changedPaths(before, after, func(rel string) bool { return keep(filepath.Join(s.repoRoot, rel)) })
	if len(changed) == 0 {
		return out, nil
	}
	dirtyBefore, err := s.dirty(ctx, filepath.Join(s.copyDir, "index"), changed)
	if err != nil {
		return out, err
	}
	dirtyAfter, err := s.dirty(ctx, "", changed)
	if err != nil {
		return out, err
	}
	for _, rel := range changed {
		switch {
		case after.conflicted(rel):
			out.Conflicted = append(out.Conflicted, s.relToRoot(rel))
		case dirtyBefore[rel]:
			// The person's own edit predates the command; a restore would discard it.
		case after.has(rel, 0) && dirtyAfter[rel]:
			out.Stale = append(out.Stale, s.relToRoot(rel))
		case !after.has(rel, 0) && exists(filepath.Join(s.repoRoot, rel)):
			out.Leftover = append(out.Leftover, s.relToRoot(rel))
		}
	}
	return out, nil
}

func (s Snapshot) relToRoot(repoRel string) string {
	rel, err := filepath.Rel(s.root, filepath.Join(s.repoRoot, repoRel))
	if err != nil {
		return repoRel
	}
	return filepath.ToSlash(rel)
}

type entryKey struct {
	path  string
	stage byte
}

type indexEntries map[entryKey]string

func (e indexEntries) has(path string, stage byte) bool {
	_, ok := e[entryKey{path, stage}]
	return ok
}

func (e indexEntries) conflicted(path string) bool {
	return e.has(path, 1) || e.has(path, 2) || e.has(path, 3)
}

func changedPaths(before, after indexEntries, keep func(string) bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, entries := range []indexEntries{before, after} {
		for key := range entries {
			if seen[key.path] || before[key] == after[key] {
				continue
			}
			seen[key.path] = true
			if keep(key.path) {
				out = append(out, key.path)
			}
		}
	}
	slices.Sort(out)
	return out
}

// entries lists the index at indexFile, or the live index when it is empty.
func (s Snapshot) entries(ctx context.Context, indexFile string) (indexEntries, error) {
	out := indexEntries{}
	opts := gitexec.Opts{Profile: gitexec.ProfileHermetic, IndexFile: indexFile}
	err := gitexec.RunRecords(ctx, s.repoRoot, []string{"ls-files", "--stage", "-z"}, opts, func(raw []byte) error {
		meta, path, ok := strings.Cut(string(raw), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || len(fields[2]) != 1 || fields[2][0] < '0' || fields[2][0] > '3' {
			return errors.New("malformed Git index entry")
		}
		out[entryKey{path, fields[2][0] - '0'}] = fields[0] + " " + fields[1]
		return nil
	})
	return out, err
}

// dirty reports which paths differ from the index at indexFile.
func (s Snapshot) dirty(ctx context.Context, indexFile string, paths []string) (map[string]bool, error) {
	out := map[string]bool{}
	opts := gitexec.Opts{Profile: gitexec.ProfileHermetic, IndexFile: indexFile}
	for start := 0; start < len(paths); start += 256 {
		args := append([]string{"--literal-pathspecs", "diff-files", "--name-only", "-z", "--"}, paths[start:min(start+256, len(paths))]...)
		if err := gitexec.RunRecords(ctx, s.repoRoot, args, opts, func(raw []byte) error {
			out[string(raw)] = true
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// copyIndex clones the index where the filesystem supports it.
func copyIndex(src, dst string) error {
	cloned, err := fileclone.Clone(src, dst)
	if err != nil || cloned {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy index: %w", err)
	}
	return out.Close()
}
