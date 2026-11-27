package native

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/tools"
)

// PythonMutationDoor adapts pythondocuments.MutationDoor onto Painted Wolf's
// single apply-and-record source mutation door in source_write.go
// (applyAgentStream, removeAgentPath, renameAgentEntry, mkdirAgentPath).
type PythonMutationDoor struct {
	tctx  tools.ToolContext
	roots map[pythonRootKey]pythondocuments.HostRootConfig

	// BeforeRemoveSource is an optional test hook invoked after the
	// destination write commits and before source removal in a modified-bytes
	// rename, allowing deterministic concurrent-source-edit tests.
	BeforeRemoveSource func()
}

type pythonRootKey struct {
	id     string
	branch string
}

var _ pythondocuments.MutationDoor = (*PythonMutationDoor)(nil)

// NewPythonMutationDoor constructs a MutationDoor that routes all Portable
// Python workspace publications through the single attributed source_write.go
// mutation door under tctx.
func NewPythonMutationDoor(tctx tools.ToolContext, roots []pythondocuments.HostRootConfig) *PythonMutationDoor {
	m := make(map[pythonRootKey]pythondocuments.HostRootConfig, len(roots))
	for _, r := range roots {
		r.Dir = filepath.Clean(r.Dir)
		m[pythonRootKey{id: r.ID, branch: r.Branch}] = r
	}
	return &PythonMutationDoor{
		tctx:  tctx,
		roots: m,
	}
}

func (d *PythonMutationDoor) rootFor(target pythonruntime.CanonicalTarget) (pythondocuments.HostRootConfig, error) {
	r, ok := d.roots[pythonRootKey{id: target.RootID, branch: target.Branch}]
	if !ok {
		return pythondocuments.HostRootConfig{}, pythonruntime.ErrnoENOENT
	}
	if r.ReadOnly {
		return pythondocuments.HostRootConfig{}, pythonruntime.ErrnoEROFS
	}
	return r, nil
}

func (d *PythonMutationDoor) mutationTargetFor(root pythondocuments.HostRootConfig, relPath string) mutationTarget {
	rel := filepath.FromSlash(relPath)
	return mutationTarget{
		Abs:      filepath.Join(root.Dir, rel),
		Location: fseffect.Location{Root: root.Dir, Rel: rel},
	}
}

// ApplyFile publishes a create or write through applyAgentStream with
// held-descriptor base-revision precondition verification.
func (d *PythonMutationDoor) ApplyFile(ctx context.Context, req pythondocuments.PublishWriteRequest) (pythondocuments.PublishResult, error) {
	root, err := d.rootFor(req.Target)
	if err != nil {
		return pythondocuments.PublishResult{}, err
	}
	target := d.mutationTargetFor(root, req.Target.RelPath)
	var conflict *pythonruntime.RevisionConflict
	res, err := applyAgentStream(ctx, d.tctx, agentStreamRequest{
		Mode:        newFileMode(req.Mode),
		Target:      target,
		Source:      bytes.NewReader(req.Bytes),
		KnownAbsent: req.Create,
		BeforeCommit: func(current fseffect.Target, _ fseffect.Result) error {
			if c := checkHeldTargetRevision(current, req.Target, req.BaseRevision); c != nil {
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

// RemovePath verifies the source base-revision precondition and removes the
// entry through removeAgentPath.
func (d *PythonMutationDoor) RemovePath(ctx context.Context, req pythondocuments.PublishDeleteRequest) (pythondocuments.PublishResult, error) {
	root, err := d.rootFor(req.Target)
	if err != nil {
		return pythondocuments.PublishResult{}, err
	}
	if c := checkLocationRevision(root.Dir, req.Target, req.BaseRevision); c != nil {
		return pythondocuments.PublishResult{Conflict: c}, &pythondocuments.RevisionConflictError{Conflict: *c}
	}
	target := d.mutationTargetFor(root, req.Target.RelPath)
	if err := removeAgentPath(ctx, d.tctx, target, nil); err != nil {
		return pythondocuments.PublishResult{}, err
	}
	return pythondocuments.PublishResult{
		CommittedRevision: pythondocuments.DocumentRevision{
			RootID: req.Target.RootID,
			Branch: req.Target.Branch,
			Absent: true,
		},
	}, nil
}

// RenamePath validates both source and target base revisions BEFORE mutating
// disk. For modified-bytes renames, if the destination write commits and
// subsequent source removal fails, the committed destination revision and byte
// count are preserved on PublishResult alongside the error/conflict so actual
// disk state and reported committed effects always agree.
func (d *PythonMutationDoor) RenamePath(ctx context.Context, req pythondocuments.PublishMoveRequest) (pythondocuments.PublishResult, error) {
	root, err := d.rootFor(req.Target)
	if err != nil {
		return pythondocuments.PublishResult{}, err
	}
	if _, err := d.rootFor(req.Source); err != nil {
		return pythondocuments.PublishResult{}, err
	}

	// Validate both source and target revision preconditions BEFORE touching disk.
	if c := checkLocationRevision(root.Dir, req.Source, req.SourceBaseRevision); c != nil {
		return pythondocuments.PublishResult{Conflict: c}, &pythondocuments.RevisionConflictError{Conflict: *c}
	}
	if c := checkLocationRevision(root.Dir, req.Target, req.TargetBaseRevision); c != nil {
		return pythondocuments.PublishResult{Conflict: c}, &pythondocuments.RevisionConflictError{Conflict: *c}
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
		if d.BeforeRemoveSource != nil {
			d.BeforeRemoveSource()
		}
		rmRes, rmErr := d.RemovePath(ctx, pythondocuments.PublishDeleteRequest{
			Target:       req.Source,
			BaseRevision: req.SourceBaseRevision,
		})
		if rmErr != nil {
			// Preserve the already-committed destination write so Settle reports
			// exact agreement with disk state alongside the partial failure.
			return pythondocuments.PublishResult{
				CommittedRevision: writeRes.CommittedRevision,
				BytesWritten:      writeRes.BytesWritten,
				Conflict:          rmRes.Conflict,
			}, rmErr
		}
		return writeRes, nil
	}

	from := d.mutationTargetFor(root, req.Source.RelPath)
	to := d.mutationTargetFor(root, req.Target.RelPath)
	if err := renameAgentEntry(ctx, d.tctx, from, to, 0, nil, nil); err != nil {
		return pythondocuments.PublishResult{}, err
	}
	f, err := fseffect.OpenRead(to.Location)
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

// MkdirPath creates a directory through mkdirAgentPath.
func (d *PythonMutationDoor) MkdirPath(ctx context.Context, req pythondocuments.PublishMkdirRequest) (pythondocuments.PublishResult, error) {
	root, err := d.rootFor(req.Target)
	if err != nil {
		return pythondocuments.PublishResult{}, err
	}
	mode := req.Mode
	if mode == 0 {
		mode = 0o755
	}
	target := d.mutationTargetFor(root, req.Target.RelPath)
	if _, err := mkdirAgentPath(ctx, d.tctx, target, mode); err != nil {
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

func checkHeldTargetRevision(
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

func checkLocationRevision(
	rootDir string,
	target pythonruntime.CanonicalTarget,
	base pythondocuments.DocumentRevision,
) *pythonruntime.RevisionConflict {
	rel := filepath.FromSlash(target.RelPath)
	abs := filepath.Join(rootDir, rel)
	info, err := os.Lstat(abs)
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
	f, err := fseffect.OpenRead(fseffect.Location{Root: rootDir, Rel: rel})
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
