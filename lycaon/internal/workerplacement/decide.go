package workerplacement

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
)

// NotClonableReason is the typed fact behind a not-clonable decision.
type NotClonableReason string

const (
	ReasonRootsSpanVolumes    NotClonableReason = "roots_span_volumes"
	ReasonVolumeCannotClone   NotClonableReason = "volume_cannot_clone"
	ReasonVolumeRootInRoot    NotClonableReason = "volume_root_inside_attached_root"
	ReasonVolumeRootReadOnly  NotClonableReason = "volume_root_unwritable"
	ReasonVolumeNotIdentified NotClonableReason = "volume_not_identified"
)

// ErrNoLocationOutsideRoots means the attached roots cover every location the
// engine could use, such as a root attached at /.
var ErrNoLocationOutsideRoots = errors.New("no worker branch location outside the attached roots")

// ErrInsideAttachedRoot refuses a placement inside an attached root.
var ErrInsideAttachedRoot = errors.New("worker branch placement is inside an attached root")

// Request carries the facts of one worker's placement.
type Request struct {
	StateRoot string
	// StateBranches is the branch base in the state folder.
	StateBranches string
	// AttachedRoots are every project and source root of the worker.
	AttachedRoots []string
	// SourceRoots are the trees preparation reads.
	SourceRoots []string
	// Probe reports whether the sources can clone into dir.
	Probe func(ctx context.Context, dir string) (bool, error)
}

// Decision says where a branch goes and whether it is expected to clone.
type Decision struct {
	// Base is the branch base: the state folder's or a placement folder's.
	Base     string
	Folder   *Folder
	Clonable bool
	Reason   NotClonableReason
}

// AssertOutsideRoots is the one rule every placement passes.
func AssertOutsideRoots(path string, roots []string) error {
	if insideAny(path, roots) {
		return fmt.Errorf("%w: %s", ErrInsideAttachedRoot, path)
	}
	return nil
}

func insideAny(path string, roots []string) bool {
	canonical := fspath.CanonicalPath(path)
	for _, root := range roots {
		if atOrUnder(canonical, fspath.CanonicalPath(root)) {
			return true
		}
	}
	return false
}

func atOrUnder(path, root string) bool {
	if root == "" {
		return false
	}
	if path == root {
		return true
	}
	return strings.HasPrefix(path, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// Decide places one worker: the state folder when it qualifies, otherwise a
// folder at the root of the sources' volume, otherwise the not-clonable path.
func Decide(ctx context.Context, req Request) (Decision, error) {
	stateBase := req.StateBranches
	volume, reason := sourcesVolume(req.SourceRoots)
	if reason != "" {
		return notClonable(ctx, req, stateBase, reason)
	}
	stateVolume, stateErr := volumeOf(existingAncestor(req.StateRoot))
	if stateErr == nil && stateVolume.ID == volume.ID && !insideAny(stateBase, req.AttachedRoots) {
		if err := os.MkdirAll(stateBase, 0o700); err != nil {
			return Decision{}, err
		}
		supported, err := req.Probe(ctx, stateBase)
		if err != nil {
			return Decision{}, err
		}
		if supported {
			return Decision{Base: stateBase, Clonable: true}, nil
		}
		return notClonable(ctx, req, stateBase, ReasonVolumeCannotClone)
	}
	return atVolumeRoot(ctx, req, stateBase, volume)
}

func atVolumeRoot(ctx context.Context, req Request, stateBase string, volume Volume) (Decision, error) {
	path := filepath.Join(volume.Root, enginepaths.WorkerPlacementDirName(os.Getuid(), req.StateRoot))
	if insideAny(path, req.AttachedRoots) {
		return notClonable(ctx, req, stateBase, ReasonVolumeRootInRoot)
	}
	folder, created, err := reserve(ctx, req.StateRoot, path, volume.Root, KindVolumeRoot)
	if err != nil {
		if unwritable(err) {
			return notClonable(ctx, req, stateBase, ReasonVolumeRootReadOnly)
		}
		return Decision{}, err
	}
	if err := os.MkdirAll(folder.BranchesRoot(), 0o700); err != nil {
		return Decision{}, err
	}
	supported, err := req.Probe(ctx, folder.BranchesRoot())
	if err != nil {
		return Decision{}, errors.Join(err, releaseIfCreated(ctx, req.StateRoot, folder, created))
	}
	if !supported {
		if err := releaseIfCreated(ctx, req.StateRoot, folder, created); err != nil {
			return Decision{}, err
		}
		return notClonable(ctx, req, stateBase, ReasonVolumeCannotClone)
	}
	return Decision{Base: folder.BranchesRoot(), Folder: &folder, Clonable: true}, nil
}

func releaseIfCreated(ctx context.Context, stateRoot string, folder Folder, created bool) error {
	if !created {
		return nil
	}
	return release(context.WithoutCancel(ctx), stateRoot, folder)
}

func unwritable(err error) bool {
	return errors.Is(err, fs.ErrPermission) || readOnlyFilesystem(err)
}

// notClonable keeps today's strategies. The tree still stays out of every root,
// so the state folder is used unless a root holds it. Sibling folders of a
// project are never candidates, because they can land in a synced tree.
func notClonable(ctx context.Context, req Request, stateBase string, reason NotClonableReason) (Decision, error) {
	if !insideAny(stateBase, req.AttachedRoots) {
		return Decision{Base: stateBase, Reason: reason}, nil
	}
	name := enginepaths.WorkerPlacementDirName(os.Getuid(), req.StateRoot)
	for _, parent := range fallbackParents() {
		path := filepath.Join(parent, name)
		if insideAny(path, req.AttachedRoots) || isSiblingOfRoot(path, req.AttachedRoots) {
			continue
		}
		folder, _, err := reserve(ctx, req.StateRoot, path, parent, KindOutsideRoots)
		if err != nil {
			if unwritable(err) || errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return Decision{}, err
		}
		if err := os.MkdirAll(folder.BranchesRoot(), 0o700); err != nil {
			return Decision{}, err
		}
		return Decision{Base: folder.BranchesRoot(), Folder: &folder, Reason: reason}, nil
	}
	return Decision{}, fmt.Errorf("%w: %v", ErrNoLocationOutsideRoots, req.AttachedRoots)
}

func isSiblingOfRoot(path string, roots []string) bool {
	parent := filepath.Dir(fspath.CanonicalPath(path))
	for _, root := range roots {
		if filepath.Dir(fspath.CanonicalPath(root)) == parent {
			return true
		}
	}
	return false
}

func sourcesVolume(sources []string) (Volume, NotClonableReason) {
	var first Volume
	for i, source := range sources {
		volume, err := volumeOf(existingAncestor(source))
		if err != nil {
			return Volume{}, ReasonVolumeNotIdentified
		}
		if i == 0 {
			first = volume
			continue
		}
		if volume.ID != first.ID {
			return Volume{}, ReasonRootsSpanVolumes
		}
	}
	if len(sources) == 0 {
		return Volume{}, ReasonVolumeNotIdentified
	}
	return first, ""
}

func existingAncestor(path string) string {
	current := fspath.CanonicalPath(path)
	for {
		if _, err := os.Stat(current); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return current
		}
		current = parent
	}
}
