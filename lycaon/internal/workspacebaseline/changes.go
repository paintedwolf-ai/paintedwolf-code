package workspacebaseline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/fseffect"
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
// RootID and ScopeRel carry the structured identity; Path is the display spelling
// kept for legacy readers.
type Change struct {
	RootID   string
	ScopeRel string
	Path     string
	Kind     ChangeKind
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

// ChangeSet is Changes with each path's kind, sorted by display path.
func (r *Reader) ChangeSet(ctx context.Context, roots []projectroot.RootRef, branch string) ([]Change, error) {
	var changes []Change
	add := func(change Change) error {
		changes = append(changes, change)
		if len(changes) > DefaultMaxOverlayFiles {
			return &OverlayBudgetExceededError{
				Files:    len(changes),
				MaxFiles: DefaultMaxOverlayFiles,
				Reason:   fmt.Sprintf("%d changed files exceeds limit of %d", len(changes), DefaultMaxOverlayFiles),
			}
		}
		return nil
	}
	err := r.EachIdentity(ctx, roots, func(entry Entry) error {
		abs, err := branchPathAt(roots, branch, entry.RootID, entry.ScopeRel)
		if err != nil {
			return err
		}
		kind, err := statChange(abs, entry.File)
		if err != nil {
			return err
		}
		if kind != "" {
			if err := add(Change{RootID: entry.RootID, ScopeRel: entry.ScopeRel, Path: entry.Path, Kind: kind}); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}

	err = Branch(roots, branch)(ctx, func(file CaptureFile) error {
		_, exists, err := r.LookupIdentity(ctx, roots, file.RootID, file.ScopeRel)
		if err != nil {
			return err
		}
		if !exists {
			return add(Change{RootID: file.RootID, ScopeRel: file.ScopeRel, Path: file.Path, Kind: ChangeCreated})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path != changes[j].Path {
			return changes[i].Path < changes[j].Path
		}
		if changes[i].RootID != changes[j].RootID {
			return changes[i].RootID < changes[j].RootID
		}
		return changes[i].ScopeRel < changes[j].ScopeRel
	})
	return changes, nil
}

// statChange compares one baseline entry with the tree by metadata alone.
func statChange(abs string, before File) (ChangeKind, error) {
	after, err := os.Lstat(abs)
	if pathGone(err) {
		return ChangeDeleted, nil
	}
	if err != nil {
		return "", err
	}
	if after.IsDir() {
		return ChangeDeleted, nil
	}
	if after.Size() != before.Size || after.ModTime().UnixNano() != before.MtimeNano {
		return ChangeModified, nil
	}
	return "", nil
}

// pathGone includes a parent that became a file, which reports ENOTDIR.
func pathGone(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR)
}

// PathComparison is one path's change with the text of each side. Kind is empty
// when the path matches its baseline. A side with no text to compare, such as an
// oversized, binary or special file, leaves its Has flag false.
type PathComparison struct {
	Kind ChangeKind
	// Absent means neither the baseline nor the tree holds the path.
	Absent        bool
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
		if err != nil && !pathGone(err) {
			return PathComparison{}, err
		}
		out.Absent = err != nil
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

// liveText reads a regular file. The open does not block, so a FIFO with no writer
// answers at once. A file it cannot read as text within the branch has no text to
// compare.
func liveText(base, rel string, kind ChangeKind) (string, bool) {
	if kind == ChangeDeleted {
		return "", true
	}
	file, err := fseffect.OpenRead(fseffect.Location{Root: base, Rel: rel})
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

// PathMove is one manifest path and the path its content may have moved to.
type PathMove struct{ From, To string }

// MoveUnchanged reports whether the file now at To has the bytes the baseline holds
// at From. A body the baseline did not pin, or a file it cannot read, is no match.
func (r *Reader) MoveUnchanged(ctx context.Context, roots []projectroot.RootRef, branch string, move PathMove) (bool, error) {
	before, inBaseline, err := r.Lookup(ctx, move.From)
	if err != nil {
		return false, err
	}
	if !inBaseline || before.Deleted || before.Opaque || before.SHA256 == "" {
		return false, nil
	}
	base, rel, err := branchLocation(roots, branch, move.To)
	if err != nil {
		return false, err
	}
	file, err := fseffect.OpenRead(fseffect.Location{Root: base, Rel: rel})
	if err != nil {
		return false, nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != before.Size {
		return false, nil
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false, nil
	}
	return hex.EncodeToString(hash.Sum(nil)) == before.SHA256, nil
}

func branchPath(roots []projectroot.RootRef, branch, path string) (string, error) {
	base, rel, err := branchLocation(roots, branch, path)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, rel), nil
}

func branchPathAt(roots []projectroot.RootRef, branch, rootID, scopeRel string) (string, error) {
	base, rel, err := branchLocationAt(roots, branch, rootID, scopeRel)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, rel), nil
}

// branchLocationAt splits a structured identity into the tree holding it and
// its place there. Multi-root trees keep one directory per root under the
// branch; single-root trees keep the root's files at the branch top.
func branchLocationAt(roots []projectroot.RootRef, branch, rootID, scopeRel string) (base, rel string, err error) {
	base = branch
	if len(roots) > 1 {
		dir, err := projectroot.BranchDirForID(rootID)
		if err != nil {
			return "", "", err
		}
		base = filepath.Join(branch, dir)
	}
	if scopeRel == "" || filepath.IsAbs(scopeRel) || sandbox.HasParentTraversal(scopeRel) {
		return "", "", fmt.Errorf("invalid baseline path")
	}
	return base, filepath.FromSlash(scopeRel), nil
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
