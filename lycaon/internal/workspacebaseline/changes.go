package workspacebaseline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textfile"
)

// ChangeKind says how a path differs from its baseline.
type ChangeKind string

const (
	ChangeCreated  ChangeKind = "created"
	ChangeModified ChangeKind = "modified"
	ChangeDeleted  ChangeKind = "deleted"
)

// Change is one path the branch tree no longer holds as the baseline recorded it.
type Change struct {
	Path string
	Kind ChangeKind
}

// Changes compares metadata without loading the complete manifest into memory.
func (r *Reader) Changes(ctx context.Context, roots []projectroot.RootRef, branch string) ([]string, error) {
	changes, err := r.ChangeSet(ctx, roots, branch)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		paths = append(paths, change.Path)
	}
	return paths, nil
}

// ChangeSet is Changes with each path's kind, sorted by path.
func (r *Reader) ChangeSet(ctx context.Context, roots []projectroot.RootRef, branch string) ([]Change, error) {
	var changes []Change
	add := func(path string, kind ChangeKind) error {
		changes = append(changes, Change{Path: path, Kind: kind})
		if len(changes) > DefaultMaxOverlayFiles {
			return &OverlayBudgetExceededError{
				Files:    len(changes),
				MaxFiles: DefaultMaxOverlayFiles,
				Reason:   fmt.Sprintf("%d changed files exceeds limit of %d", len(changes), DefaultMaxOverlayFiles),
			}
		}
		return nil
	}
	err := r.Each(ctx, func(path string, before File) error {
		abs, err := branchPath(roots, branch, path)
		if err != nil {
			return err
		}
		kind, err := statChange(abs, before)
		if err != nil {
			return err
		}
		if kind != "" {
			if err := add(path, kind); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}

	err = Branch(roots, branch)(ctx, func(file CaptureFile) error {
		_, exists, err := r.Lookup(ctx, file.Path)
		if err != nil {
			return err
		}
		if !exists {
			return add(file.Path, ChangeCreated)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

// statChange compares one baseline entry with the tree by metadata alone.
func statChange(abs string, before File) (ChangeKind, error) {
	after, err := os.Lstat(abs)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err != nil || after.IsDir() {
		return ChangeDeleted, nil
	}
	if after.Size() != before.Size || after.ModTime().UnixNano() != before.MtimeNano {
		return ChangeModified, nil
	}
	return "", nil
}

// PathComparison is one path's change with the text of each side. Kind is empty
// when the path matches its baseline. A side with no text to compare, such as an
// oversized, binary or special file, leaves its Has flag false.
type PathComparison struct {
	Kind          ChangeKind
	Abs           string
	Before, After string
	HasBefore     bool
	HasAfter      bool
}

// ComparePath answers for one path without walking the tree.
func (r *Reader) ComparePath(ctx context.Context, roots []projectroot.RootRef, branch, path string) (PathComparison, error) {
	base, rel, err := branchLocation(roots, branch, path)
	if err != nil {
		return PathComparison{}, err
	}
	abs := filepath.Join(base, rel)
	out := PathComparison{Abs: abs}
	before, inBaseline, err := r.Lookup(ctx, path)
	if err != nil {
		return PathComparison{}, err
	}
	if inBaseline {
		out.Kind, err = statChange(abs, before)
		if err != nil {
			return PathComparison{}, err
		}
	} else {
		info, err := os.Lstat(abs)
		if err != nil && !os.IsNotExist(err) {
			return PathComparison{}, err
		}
		if err == nil && !info.IsDir() {
			out.Kind = ChangeCreated
		}
	}
	if out.Kind == "" {
		return out, nil
	}
	out.Before, out.HasBefore = r.baselineText(ctx, path, before, inBaseline)
	out.After, out.HasAfter = liveText(base, rel, out.Kind)
	return out, nil
}

// baselineText treats a body it cannot decode as having no text to compare.
func (r *Reader) baselineText(ctx context.Context, path string, before File, inBaseline bool) (string, bool) {
	if !inBaseline {
		return "", true
	}
	if before.Opaque || before.SHA256 == "" {
		return "", false
	}
	text, _, err := r.Content(ctx, path)
	if err != nil {
		return "", false
	}
	return text, true
}

// liveText reads a regular file. A file it cannot read as text within the branch
// has no text to compare.
func liveText(base, rel string, kind ChangeKind) (string, bool) {
	if kind == ChangeDeleted {
		return "", true
	}
	file, err := os.OpenInRoot(base, rel)
	if err != nil {
		return "", false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxContentBytes {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(file, MaxContentBytes+1))
	if err != nil || len(body) > MaxContentBytes {
		return "", false
	}
	doc, _, err := textfile.Open(body, textfile.LimitsForRaw(MaxContentBytes))
	if err != nil {
		return "", false
	}
	return doc.Text(), true
}

func branchPath(roots []projectroot.RootRef, branch, path string) (string, error) {
	base, rel, err := branchLocation(roots, branch, path)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, rel), nil
}

// branchLocation splits a manifest path into the tree holding it and its place there.
func branchLocation(roots []projectroot.RootRef, branch, path string) (base, rel string, err error) {
	base = branch
	if len(roots) > 1 {
		primary, err := projectroot.PrimaryRoot(roots)
		if err != nil {
			return "", "", err
		}
		chosen := primary
		if strings.HasPrefix(path, "@") {
			label, rel, ok := strings.Cut(path[1:], "/")
			if !ok {
				return "", "", fmt.Errorf("invalid qualified baseline path")
			}
			found := false
			for _, root := range roots {
				if strings.EqualFold(root.Label, label) {
					chosen = root
					found = true
					break
				}
			}
			if !found {
				return "", "", fmt.Errorf("unknown baseline root")
			}
			path = rel
		}
		dir, err := projectroot.BranchDirForID(chosen.ID)
		if err != nil {
			return "", "", err
		}
		base = filepath.Join(branch, dir)
	}
	if path == "" || filepath.IsAbs(path) || sandbox.HasParentTraversal(path) {
		return "", "", fmt.Errorf("invalid baseline path")
	}
	return base, filepath.FromSlash(path), nil
}

// CanonicalPath maps a manifest path onto the project root it was captured
// from, the same way branchPath maps it onto the branch tree.
func CanonicalPath(roots []projectroot.RootRef, path string) (string, error) {
	if len(roots) == 0 {
		return "", fmt.Errorf("project roots required")
	}
	chosen := roots[0]
	if len(roots) > 1 {
		primary, err := projectroot.PrimaryRoot(roots)
		if err != nil {
			return "", err
		}
		chosen = primary
		if strings.HasPrefix(path, "@") {
			label, rel, ok := strings.Cut(path[1:], "/")
			if !ok {
				return "", fmt.Errorf("invalid qualified baseline path")
			}
			found := false
			for _, root := range roots {
				if strings.EqualFold(root.Label, label) {
					chosen = root
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("unknown baseline root")
			}
			path = rel
		}
	}
	if path == "" || filepath.IsAbs(path) || sandbox.HasParentTraversal(path) {
		return "", fmt.Errorf("invalid baseline path")
	}
	return filepath.Join(chosen.Path, filepath.FromSlash(path)), nil
}
