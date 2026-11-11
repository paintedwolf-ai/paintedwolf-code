package workspace

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

// A layered branch answers a worker's own change list from its view: a mounted view's journal, or a
// materialized view's stat capture against its pristine tree. Each answer reports false when the
// branch has no view, and the caller falls back to the branch's baseline.

// branchView is the view behind a branch, with its core.
func branchView(ctx context.Context, branchRoot string) (*adapter.PreparedView, bool, error) {
	if view, _, _, ok := LookupBranchPreparedView(branchRoot); ok && view != nil && view.Core != nil {
		return view, true, nil
	}
	view, ok, err := materializedViewOf(ctx, branchRoot)
	if err != nil || !ok || view == nil || view.Core == nil {
		return nil, false, err
	}
	return view, true, nil
}

// BranchTreeChanges lists the files a layered branch changed against its base, with how each changed.
func BranchTreeChanges(ctx context.Context, branchRoot string, roots []projectroot.RootRef) ([]workspacebaseline.Change, bool, error) {
	view, ok, err := branchView(ctx, branchRoot)
	if !ok || err != nil {
		return nil, ok, err
	}
	var changes []workspacebaseline.Change
	add := func(qp layeredfs.QualifiedPath, kind workspacebaseline.ChangeKind) {
		if consumer.IsInternalStoragePath(qp.Rel) {
			return
		}
		if path, ok := manifestPathFor(roots, qp); ok {
			changes = append(changes, workspacebaseline.Change{Path: path, Kind: kind})
		}
	}
	// A path the worker created and then removed was never in the base, so it is no change.
	addDeleted := func(qp layeredfs.QualifiedPath) error {
		inBase, err := view.Core.HasBase(qp)
		if err == nil && inBase {
			add(qp, workspacebaseline.ChangeDeleted)
		}
		return err
	}
	if eng, found := consumer.LookupBranchEngine(branchRoot); found && eng != nil {
		summary, err := eng.ComputeDivergence(ctx, "")
		if err != nil {
			return nil, true, err
		}
		for _, entry := range summary.Entries {
			if entry.EntryKind == layeredfs.EntryDirectory {
				continue
			}
			switch entry.Kind {
			case consumer.DivergenceAdded:
				add(entry.Path, workspacebaseline.ChangeCreated)
			case consumer.DivergenceDeleted:
				if err := addDeleted(entry.Path); err != nil {
					return nil, true, err
				}
			case consumer.DivergenceRenamed:
				if entry.PreviousPath != nil {
					if err := addDeleted(*entry.PreviousPath); err != nil {
						return nil, true, err
					}
				}
				add(entry.Path, workspacebaseline.ChangeCreated)
			default:
				add(entry.Path, workspacebaseline.ChangeModified)
			}
		}
	} else {
		changed, _, err := adapter.NewEagerCopyAdapter().ChangedPaths(ctx, view.ViewID)
		if err != nil {
			return nil, true, err
		}
		for _, qp := range changed {
			path, ok := manifestPathFor(roots, qp)
			if !ok {
				continue
			}
			loc, err := workspacebaseline.LocateInBranch(roots, branchRoot, path)
			if err != nil {
				return nil, true, err
			}
			kind, err := viewChangeKind(view.Core, qp, loc.Abs)
			if err != nil {
				return nil, true, err
			}
			if kind != "" {
				add(qp, kind)
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, true, nil
}

// viewChangeKind classifies one changed path by whether its base and the branch tree hold it. The
// branch tree is read where a worker sees it, which for a materialized view is not its core.
func viewChangeKind(core *layeredfs.Core, qp layeredfs.QualifiedPath, abs string) (workspacebaseline.ChangeKind, error) {
	inBase, err := core.HasBase(qp)
	if err != nil {
		return "", err
	}
	live, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if inBase {
			return workspacebaseline.ChangeDeleted, nil
		}
		return "", nil
	case err != nil:
		return "", err
	case live.IsDir():
		return "", nil
	case inBase:
		return workspacebaseline.ChangeModified, nil
	default:
		return workspacebaseline.ChangeCreated, nil
	}
}

// BranchPathChange compares one path of a layered branch with its base, reading only that path.
func BranchPathChange(ctx context.Context, branchRoot string, roots []projectroot.RootRef, path string) (workspacebaseline.PathComparison, bool, error) {
	view, ok, err := branchView(ctx, branchRoot)
	if !ok || err != nil {
		return workspacebaseline.PathComparison{}, ok, err
	}
	loc, err := workspacebaseline.LocateInBranch(roots, branchRoot, path)
	if err != nil {
		return workspacebaseline.PathComparison{}, true, err
	}
	qp := layeredfs.QualifiedPath{RootID: loc.RootID, Rel: loc.Rel}
	kind, err := viewChangeKind(view.Core, qp, loc.Abs)
	if err != nil {
		return workspacebaseline.PathComparison{}, true, err
	}
	out := workspacebaseline.PathComparison{Abs: loc.Abs, Kind: kind}
	if kind == "" {
		if _, err := os.Lstat(loc.Abs); errors.Is(err, fs.ErrNotExist) {
			out.Absent = true
		}
		return out, true, nil
	}
	after, hasAfter := liveBody(loc.Abs, kind)
	if kind == workspacebaseline.ChangeModified && hasAfter && sameAsBase(view.Core, qp, after) {
		return workspacebaseline.PathComparison{Abs: loc.Abs}, true, nil
	}
	out.Before, out.HasBefore = baseText(view.Core, qp, kind)
	if kind == workspacebaseline.ChangeDeleted {
		out.HasAfter = true
	} else if hasAfter {
		out.After, out.HasAfter = decodedText(after)
	}
	return out, true, nil
}

// BranchMovesUnchanged names the moves whose live file holds the bytes the base held at the source.
func BranchMovesUnchanged(ctx context.Context, branchRoot string, roots []projectroot.RootRef, moves []workspacebaseline.PathMove) (map[workspacebaseline.PathMove]bool, bool, error) {
	view, ok, err := branchView(ctx, branchRoot)
	if !ok || err != nil {
		return nil, ok, err
	}
	unchanged := make(map[workspacebaseline.PathMove]bool, len(moves))
	for _, move := range moves {
		from, err := workspacebaseline.LocateInBranch(roots, branchRoot, move.From)
		if err != nil {
			return nil, true, err
		}
		to, err := workspacebaseline.LocateInBranch(roots, branchRoot, move.To)
		if err != nil {
			return nil, true, err
		}
		before, inBase, err := view.Core.ReadBase(layeredfs.QualifiedPath{RootID: from.RootID, Rel: from.Rel}, workspacebaseline.MaxContentBytes)
		if err != nil || !inBase {
			continue
		}
		after, ok := liveBody(to.Abs, workspacebaseline.ChangeModified)
		if ok && sha256.Sum256(before) == sha256.Sum256(after) {
			unchanged[move] = true
		}
	}
	return unchanged, true, nil
}

func sameAsBase(core *layeredfs.Core, qp layeredfs.QualifiedPath, after []byte) bool {
	before, inBase, err := core.ReadBase(qp, workspacebaseline.MaxContentBytes)
	return err == nil && inBase && sha256.Sum256(before) == sha256.Sum256(after)
}

// baseText is the base's text; a created file had none, and a body that is not text has no text to
// compare.
func baseText(core *layeredfs.Core, qp layeredfs.QualifiedPath, kind workspacebaseline.ChangeKind) (string, bool) {
	if kind == workspacebaseline.ChangeCreated {
		return "", true
	}
	body, inBase, err := core.ReadBase(qp, workspacebaseline.MaxContentBytes)
	if err != nil || !inBase {
		return "", false
	}
	return decodedText(body)
}

// liveBody reads a regular file of the branch tree within the content limit. The open does not block,
// so a FIFO with no writer answers at once.
func liveBody(abs string, kind workspacebaseline.ChangeKind) ([]byte, bool) {
	if kind == workspacebaseline.ChangeDeleted {
		return nil, false
	}
	file, err := os.OpenFile(abs, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > workspacebaseline.MaxContentBytes {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(file, workspacebaseline.MaxContentBytes+1))
	if err != nil || len(body) > workspacebaseline.MaxContentBytes {
		return nil, false
	}
	return body, true
}

func decodedText(body []byte) (string, bool) {
	doc, _, err := textfile.Open(body, textfile.LimitsForRaw(workspacebaseline.MaxContentBytes))
	if err != nil {
		return "", false
	}
	return doc.Text(), true
}

// manifestPathFor names a view path as a baseline manifest does: bare in the primary root, and
// @label/rel in any other.
func manifestPathFor(roots []projectroot.RootRef, qp layeredfs.QualifiedPath) (string, bool) {
	if len(roots) <= 1 {
		return qp.Rel, true
	}
	primary, err := projectroot.PrimaryRoot(roots)
	if err != nil {
		return "", false
	}
	if primary.ID == qp.RootID {
		return qp.Rel, true
	}
	for _, root := range roots {
		if root.ID == qp.RootID {
			return "@" + root.Label + "/" + qp.Rel, true
		}
	}
	return "", false
}
