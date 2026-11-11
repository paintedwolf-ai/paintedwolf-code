package workspacebaseline

import (
	"bytes"
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
	err := r.eachChange(ctx, roots, branch, func(change Change) error {
		changes = append(changes, change)
		if len(changes) > DefaultMaxOverlayFiles {
			return &OverlayBudgetExceededError{
				Files:    len(changes),
				MaxFiles: DefaultMaxOverlayFiles,
				Reason:   fmt.Sprintf("%d changed files exceeds limit of %d", len(changes), DefaultMaxOverlayFiles),
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

// EachChange visits every path that differs from the manifest, whatever their number.
func (r *Reader) EachChange(ctx context.Context, roots []projectroot.RootRef, branch string, visit func(path string) error) error {
	return r.eachChange(ctx, roots, branch, func(change Change) error { return visit(change.Path) })
}

func (r *Reader) eachChange(ctx context.Context, roots []projectroot.RootRef, branch string, visit func(Change) error) error {
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
			if err := visit(Change{Path: path, Kind: kind}); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	return Branch(roots, branch)(ctx, func(file CaptureFile) error {
		_, exists, err := r.Lookup(ctx, file.Path)
		if err != nil {
			return err
		}
		if !exists {
			return visit(Change{Path: file.Path, Kind: ChangeCreated})
		}
		return nil
	})
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

// BranchLocation is where one manifest path lives: its project root and the branch tree file.
type BranchLocation struct {
	RootID string
	Rel    string
	Abs    string
}

// LocateInBranch maps a manifest path onto its root and branch tree file.
func LocateInBranch(roots []projectroot.RootRef, branch, path string) (BranchLocation, error) {
	if len(roots) == 0 {
		return BranchLocation{}, fmt.Errorf("project roots required")
	}
	chosen, rel, err := manifestRoot(roots, path)
	if err != nil {
		return BranchLocation{}, err
	}
	base, treeRel, err := branchLocation(roots, branch, path)
	if err != nil {
		return BranchLocation{}, err
	}
	return BranchLocation{RootID: chosen.ID, Rel: rel, Abs: filepath.Join(base, treeRel)}, nil
}

// manifestRoot resolves a manifest path to its root and root-relative path. A multi-root manifest
// qualifies paths outside the primary root as @label/rel.
func manifestRoot(roots []projectroot.RootRef, path string) (projectroot.RootRef, string, error) {
	if len(roots) == 1 {
		return roots[0], path, nil
	}
	primary, err := projectroot.PrimaryRoot(roots)
	if err != nil {
		return projectroot.RootRef{}, "", err
	}
	if !strings.HasPrefix(path, "@") {
		return primary, path, nil
	}
	label, rel, ok := strings.Cut(path[1:], "/")
	if !ok {
		return projectroot.RootRef{}, "", fmt.Errorf("invalid qualified baseline path")
	}
	for _, root := range roots {
		if strings.EqualFold(root.Label, label) {
			return root, rel, nil
		}
	}
	return projectroot.RootRef{}, "", fmt.Errorf("unknown baseline root")
}

// branchLocation splits a manifest path into the tree holding it and its place there.
func branchLocation(roots []projectroot.RootRef, branch, path string) (base, rel string, err error) {
	base = branch
	if len(roots) > 1 {
		chosen, rootRel, err := manifestRoot(roots, path)
		if err != nil {
			return "", "", err
		}
		path = rootRel
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
	chosen, rel, err := manifestRoot(roots, path)
	if err != nil {
		return "", err
	}
	if rel == "" || filepath.IsAbs(rel) || sandbox.HasParentTraversal(rel) {
		return "", fmt.Errorf("invalid baseline path")
	}
	return filepath.Join(chosen.Path, filepath.FromSlash(rel)), nil
}

// OverlayChange is one path a sealed overlay records, against its baseline.
type OverlayChange struct {
	RootID     string
	Rel        string
	Final      File
	Base       File
	BaseExists bool
	// Body reads pinned content by digest.
	Body func(sha string) ([]byte, error)
}

// EachOverlayChange visits a sealed overlay's paths with their baseline state. It reads only the
// immutable manifests and their pinned content, never a working tree.
func EachOverlayChange(ctx context.Context, baselinePath, overlayPath string, roots []projectroot.RootRef, visit func(OverlayChange) error) error {
	blobs := ContentStore(baselinePath)
	baseline, err := Open(ctx, baselinePath, blobs)
	if err != nil {
		return err
	}
	defer func() { _ = baseline.Close() }()
	overlay, err := Open(ctx, overlayPath, blobs)
	if err != nil {
		return err
	}
	defer func() { _ = overlay.Close() }()
	body := func(sha string) ([]byte, error) {
		var content bytes.Buffer
		err := blobs.CopySHA(ctx, sha, &content)
		return content.Bytes(), err
	}
	return overlay.Each(ctx, func(path string, final File) error {
		root, rel, err := manifestRoot(roots, path)
		if err != nil {
			return err
		}
		base, exists, err := baseline.Lookup(ctx, path)
		if err != nil {
			return err
		}
		if final.Opaque && final.LinkTarget == "" {
			return fmt.Errorf("overlay body for %s was not captured", path)
		}
		return visit(OverlayChange{RootID: root.ID, Rel: rel, Final: final, Base: base, BaseExists: exists, Body: body})
	})
}
