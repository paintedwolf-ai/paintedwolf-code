package portablepython

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
)

// fsEffectMutationDoor adapts pythondocuments.MutationDoor onto Painted Wolf's
// single atomic fseffect door with base-revision precondition verification.
type fsEffectMutationDoor struct {
	roots map[rootKey]pythondocuments.HostRootConfig
}

type rootKey struct {
	id     string
	branch string
}

var _ pythondocuments.MutationDoor = (*fsEffectMutationDoor)(nil)

func newFSEffectMutationDoor(roots []pythondocuments.HostRootConfig) *fsEffectMutationDoor {
	m := make(map[rootKey]pythondocuments.HostRootConfig, len(roots))
	for _, r := range roots {
		r.Dir = filepath.Clean(r.Dir)
		m[rootKey{id: r.ID, branch: r.Branch}] = r
	}
	return &fsEffectMutationDoor{roots: m}
}

func (d *fsEffectMutationDoor) rootFor(target pythonruntime.CanonicalTarget) (pythondocuments.HostRootConfig, error) {
	r, ok := d.roots[rootKey{id: target.RootID, branch: target.Branch}]
	if !ok {
		return pythondocuments.HostRootConfig{}, pythonruntime.ErrnoENOENT
	}
	if r.ReadOnly {
		return pythondocuments.HostRootConfig{}, pythonruntime.ErrnoEROFS
	}
	return r, nil
}

func (d *fsEffectMutationDoor) ApplyFile(_ context.Context, req pythondocuments.PublishWriteRequest) (pythondocuments.PublishResult, error) {
	root, err := d.rootFor(req.Target)
	if err != nil {
		return pythondocuments.PublishResult{}, err
	}
	mode := req.Mode
	if mode == 0 {
		mode = 0o644
	}
	loc := fseffect.Location{Root: root.Dir, Rel: filepath.FromSlash(req.Target.RelPath)}
	var conflict *pythonruntime.RevisionConflict
	res, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location:     loc,
		Source:       bytes.NewReader(req.Bytes),
		Mode:         mode,
		PreserveMode: !req.Create,
		BeforeCommit: func(current fseffect.Target, _ fseffect.Result) error {
			if c := checkDiskRevisionPrecondition(current, req.Target, req.BaseRevision); c != nil {
				conflict = c
				return &pythondocuments.RevisionConflictError{Conflict: *c}
			}
			return nil
		},
	})
	if err != nil {
		return pythondocuments.PublishResult{Conflict: conflict}, err
	}
	return pythondocuments.PublishResult{
		CommittedRevision: pythondocuments.DocumentRevision{
			RootID:           req.Target.RootID,
			Branch:           req.Target.Branch,
			DocumentRevision: "disk:" + res.SHA256,
			DiskSHA256:       res.SHA256,
		},
		BytesWritten: res.Bytes,
	}, nil
}

func (d *fsEffectMutationDoor) RemovePath(_ context.Context, req pythondocuments.PublishDeleteRequest) (pythondocuments.PublishResult, error) {
	root, err := d.rootFor(req.Target)
	if err != nil {
		return pythondocuments.PublishResult{}, err
	}
	loc := fseffect.Location{Root: root.Dir, Rel: filepath.FromSlash(req.Target.RelPath)}
	var conflict *pythonruntime.RevisionConflict
	err = fseffect.Remove(fseffect.RemoveRequest{
		Location: loc,
		BeforeCommit: func(current fseffect.Target) error {
			if c := checkDiskRevisionPrecondition(current, req.Target, req.BaseRevision); c != nil {
				conflict = c
				return &pythondocuments.RevisionConflictError{Conflict: *c}
			}
			return nil
		},
	})
	if err != nil {
		return pythondocuments.PublishResult{Conflict: conflict}, err
	}
	return pythondocuments.PublishResult{
		CommittedRevision: pythondocuments.DocumentRevision{
			RootID: req.Target.RootID,
			Branch: req.Target.Branch,
			Absent: true,
		},
	}, nil
}

func (d *fsEffectMutationDoor) RenamePath(ctx context.Context, req pythondocuments.PublishMoveRequest) (pythondocuments.PublishResult, error) {
	root, err := d.rootFor(req.Target)
	if err != nil {
		return pythondocuments.PublishResult{}, err
	}
	if req.HasModifiedBytes {
		writeRes, err := d.ApplyFile(ctx, pythondocuments.PublishWriteRequest{
			Target:       req.Target,
			BaseRevision: req.TargetBaseRevision,
			Bytes:        req.Bytes,
			Mode:         req.Mode,
			Create:       req.TargetBaseRevision.Absent,
		})
		if err != nil {
			return writeRes, err
		}
		if _, err := d.RemovePath(ctx, pythondocuments.PublishDeleteRequest{
			Target:       req.Source,
			BaseRevision: req.SourceBaseRevision,
		}); err != nil {
			return pythondocuments.PublishResult{}, err
		}
		return writeRes, nil
	}

	srcLoc := fseffect.Location{Root: root.Dir, Rel: filepath.FromSlash(req.Source.RelPath)}
	dstLoc := fseffect.Location{Root: root.Dir, Rel: filepath.FromSlash(req.Target.RelPath)}
	srcAbs := filepath.Join(root.Dir, filepath.FromSlash(req.Source.RelPath))
	identity, err := fspath.EntryIdentity(srcAbs)
	if err != nil {
		return pythondocuments.PublishResult{}, err
	}
	if req.TargetBaseRevision.Absent {
		err = fseffect.RelocateGuarded(srcLoc, dstLoc, identity)
	} else {
		err = fseffect.ReplaceByRelocateGuarded(srcLoc, dstLoc, identity)
	}
	if err != nil {
		return pythondocuments.PublishResult{}, err
	}
	f, err := fseffect.OpenRead(dstLoc)
	if err != nil {
		return pythondocuments.PublishResult{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return pythondocuments.PublishResult{}, err
	}
	sha := pythondocuments.SHA256Hex(data)
	return pythondocuments.PublishResult{
		CommittedRevision: pythondocuments.DocumentRevision{
			RootID:           req.Target.RootID,
			Branch:           req.Target.Branch,
			DocumentRevision: "disk:" + sha,
			DiskSHA256:       sha,
		},
		BytesWritten: int64(len(data)),
	}, nil
}

func (d *fsEffectMutationDoor) MkdirPath(_ context.Context, req pythondocuments.PublishMkdirRequest) (pythondocuments.PublishResult, error) {
	root, err := d.rootFor(req.Target)
	if err != nil {
		return pythondocuments.PublishResult{}, err
	}
	mode := req.Mode
	if mode == 0 {
		mode = 0o755
	}
	loc := fseffect.Location{Root: root.Dir, Rel: filepath.FromSlash(req.Target.RelPath)}
	if _, err := fseffect.MkdirAll(loc, mode); err != nil {
		return pythondocuments.PublishResult{}, err
	}
	return pythondocuments.PublishResult{
		CommittedRevision: pythondocuments.DocumentRevision{
			RootID:           req.Target.RootID,
			Branch:           req.Target.Branch,
			DocumentRevision: "dir:created",
		},
	}, nil
}

func checkDiskRevisionPrecondition(
	current fseffect.Target,
	target pythonruntime.CanonicalTarget,
	base pythondocuments.DocumentRevision,
) *pythonruntime.RevisionConflict {
	info, err := current.Lstat()
	if os.IsNotExist(err) {
		if base.Absent {
			return nil
		}
		return &pythonruntime.RevisionConflict{
			Target:           target,
			ExpectedRevision: base.Identity(),
			ActualRevision:   "absent",
		}
	}
	if err != nil {
		return &pythonruntime.RevisionConflict{
			Target:           target,
			ExpectedRevision: base.Identity(),
			ActualRevision:   fmt.Sprintf("stat-error:%v", err),
		}
	}
	if base.Absent {
		return &pythonruntime.RevisionConflict{
			Target:           target,
			ExpectedRevision: base.Identity(),
			ActualRevision:   "exists",
		}
	}
	if info.IsDir() || base.DiskSHA256 == "" {
		return nil
	}
	f, err := current.Open()
	if err != nil {
		return &pythonruntime.RevisionConflict{
			Target:           target,
			ExpectedRevision: base.Identity(),
			ActualRevision:   fmt.Sprintf("open-error:%v", err),
		}
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return &pythonruntime.RevisionConflict{
			Target:           target,
			ExpectedRevision: base.Identity(),
			ActualRevision:   fmt.Sprintf("read-error:%v", err),
		}
	}
	actualSHA := pythondocuments.SHA256Hex(data)
	if actualSHA != base.DiskSHA256 {
		return &pythonruntime.RevisionConflict{
			Target:           target,
			ExpectedRevision: base.Identity(),
			ActualRevision:   "rev=disk:" + actualSHA + "|sha256=" + actualSHA,
		}
	}
	return nil
}
