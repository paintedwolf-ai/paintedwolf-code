package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/workerplacement"
)

// ErrBranchVolumeUnavailable reports a branch whose volume is absent. Its records
// stay; nothing is deleted or rebuilt until the volume returns.
var ErrBranchVolumeUnavailable = errors.New("worker branch volume is unavailable")

// branchVolumeAvailable runs before anything creates directories under a placed
// branch, since a bare mount point would otherwise take them.
func branchVolumeAvailable(branchRoot string) error {
	folder := enginepaths.WorkerPlacementFolderOf(branchRoot)
	if folder == "" || workerplacement.FolderPresent(folder) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrBranchVolumeUnavailable, branchRoot)
}

// With no regular file to try, the volume itself answers.
func probeSourcesClone(ctx context.Context, sample, dir string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if sample == "" {
		return fileclone.SupportedAt(dir)
	}
	return probeClone(sample, dir)
}

func (m *Manager) decidePlacement(ctx context.Context, roots, sources []projectroot.RootRef) (workerplacement.Decision, error) {
	if strings.TrimSpace(m.branchRoot) == "" {
		return workerplacement.Decision{}, fmt.Errorf("worker branch root is unavailable")
	}
	sourcePaths := make([]string, 0, len(sources))
	for _, source := range sources {
		sourcePaths = append(sourcePaths, source.Path)
	}
	return workerplacement.Decide(ctx, workerplacement.Request{
		StateRoot:     m.stateRoot,
		AttachedRoots: placementRoots(roots, sources),
		SourceRoots:   sourcePaths,
		Probe: func(ctx context.Context, dir string) (bool, error) {
			var sample string
			for _, source := range sources {
				found, err := firstRegularFile(ctx, source.Path, m.sourceAdmit(source.Path))
				if err != nil {
					return false, err
				}
				if found != "" {
					sample = found
					break
				}
			}
			return probeSourcesClone(ctx, sample, dir)
		},
	})
}

func placementRoots(roots, sources []projectroot.RootRef) []string {
	out := make([]string, 0, len(roots)+len(sources))
	for _, root := range roots {
		out = append(out, root.Path)
	}
	for _, source := range sources {
		out = append(out, source.Path)
	}
	return out
}

// completeBranch returns a complete branch already prepared for this job.
func (m *Manager) completeBranch(logicalRoot string, layout SandboxLayout) (string, error) {
	projectKey, jobID := branchIdentity(logicalRoot)
	physical, placed, err := workerplacement.PhysicalBranchRoot(m.stateRoot, projectKey, jobID)
	if err != nil {
		return "", err
	}
	if !placed {
		physical = logicalRoot
	}
	if err := branchVolumeAvailable(physical); err != nil {
		return "", err
	}
	if completeBranchMatchesLayout(physical, layout) {
		return physical, nil
	}
	return "", nil
}

// createPlacedSkeleton records the placement before any tree exists there, so
// nothing placed is ever unknown to the registry. A placed branch also keeps a
// layout record under its logical address: backups retain it, and on another
// device, where no registry names the volume, the job rebuilds from it.
func (m *Manager) createPlacedSkeleton(ctx context.Context, logicalRoot, branchRoot string, layout SandboxLayout, jobID string, decision workerplacement.Decision) (*Binding, error) {
	if decision.Folder != nil {
		projectKey, _ := branchIdentity(logicalRoot)
		if err := workerplacement.RecordBranch(ctx, m.stateRoot, decision.Folder.ID, projectKey, jobID); err != nil {
			return nil, err
		}
		if err := WriteJobMeta(enginepaths.MetaDirForBranchRoot(logicalRoot), jobMetaFromLayout(layout)); err != nil {
			return nil, errors.Join(err, forgetPlacedBranch(m.stateRoot, branchRoot))
		}
	}
	binding, err := m.createBranchSkeleton(ctx, branchRoot, layout, jobID)
	if err != nil {
		return nil, errors.Join(err, forgetPlacedBranch(m.stateRoot, branchRoot))
	}
	return binding, nil
}

func branchIdentity(branchRoot string) (projectKey, jobID string) {
	branchRoot = filepath.Clean(branchRoot)
	return filepath.Base(filepath.Dir(branchRoot)), filepath.Base(branchRoot)
}

// forgetPlacedBranch clears what a placed branch keeps in the state folder.
func forgetPlacedBranch(stateRoot, branchRoot string) error {
	if enginepaths.WorkerPlacementFolderOf(branchRoot) == "" {
		return nil
	}
	projectKey, jobID := branchIdentity(branchRoot)
	logical := filepath.Join(enginepaths.WorkerBranchesRootUnder(stateRoot), projectKey, jobID)
	_ = os.RemoveAll(enginepaths.MetaDirForBranchRoot(logical))
	removeBranchLocks(logical)
	return workerplacement.ForgetBranch(context.Background(), stateRoot, projectKey, jobID)
}

// placedTreeSuperseded reports a placed tree that restore replaced with one in
// the state folder, which is the copy every read now resolves to.
func placedTreeSuperseded(stateRoot, branchRoot string) bool {
	if enginepaths.WorkerPlacementFolderOf(branchRoot) == "" {
		return false
	}
	projectKey, jobID := branchIdentity(branchRoot)
	_, err := os.Lstat(filepath.Join(enginepaths.WorkerBranchesRootUnder(stateRoot), projectKey, jobID))
	return err == nil
}

// branchBases lists the state base and every reachable placement base.
func branchBases(stateRoot, branchRoot string) []string {
	bases := []string{branchRoot}
	folders, err := workerplacement.AvailableFolders(stateRoot)
	if err != nil {
		return bases
	}
	for _, folder := range folders {
		bases = append(bases, folder.BranchesRoot())
	}
	return bases
}

func listAllSandboxEntries(stateRoot, branchRoot, primaryDir string) (sandboxListing, error) {
	var out sandboxListing
	for _, base := range branchBases(stateRoot, branchRoot) {
		listing, err := listSandboxEntries(base, primaryDir)
		if err != nil {
			return sandboxListing{}, err
		}
		out.jobs = append(out.jobs, listing.jobs...)
		out.metas = append(out.metas, listing.metas...)
	}
	return out, nil
}

// sourceAdmit excludes engine storage that a broad root would otherwise copy: the
// state folder holds the store, credentials and earlier branches.
type sourceAdmit = func(rel, abs string, isDir bool) bool

func (m *Manager) sourceAdmit(sourceRoot string) sourceAdmit {
	excluded := map[string]struct{}{}
	source := fspath.CanonicalPath(sourceRoot)
	for _, path := range []string{m.stateRoot, m.seedRoot} {
		if strings.TrimSpace(path) == "" {
			continue
		}
		rel, err := filepath.Rel(source, fspath.CanonicalPath(path))
		if err == nil && rel != "." && filepath.IsLocal(rel) {
			excluded[filepath.ToSlash(rel)] = struct{}{}
		}
	}
	return func(rel, _ string, isDir bool) bool {
		if !isDir || len(excluded) == 0 {
			return true
		}
		_, skip := excluded[rel]
		return !skip
	}
}

// skipUnreadable counts a directory this user cannot list instead of failing the
// walk. Home directories and volume roots hold such entries.
func skipUnreadable(count *atomic.Int64) func(rel string, err error) error {
	return func(rel string, err error) error {
		if rel == "." || !errors.Is(err, fs.ErrPermission) {
			return err
		}
		if count != nil {
			count.Add(1)
		}
		return nil
	}
}

func sourceUnreadable(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return errors.Is(err, fs.ErrPermission)
	}
	_ = file.Close()
	return false
}

// sourceCost is what a copy of one source costs in entries and bytes.
type sourceCost struct {
	entries int64
	bytes   int64
}

// declareCopyCost surveys by stat, reports the cost before the first byte moves,
// and refuses only when the destination cannot hold it.
func declareCopyCost(ctx context.Context, sourceRoot, branchRoot string, admit sourceAdmit, progress PreparationProgress) (sourceCost, error) {
	progress.Stage = "surveying_source"
	reportPreparation(ctx, progress)
	var cost sourceCost
	err := sandbox.SurveyWalk(ctx, sourceRoot, sandbox.SurveyOptions{IncludeHidden: true, Admit: admit, OnReadError: skipUnreadable(nil)},
		func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			info, err := entry.DirEntry.Info()
			if err != nil {
				if sourceChangeSkippable(err) {
					return sandbox.SurveyContinue, nil
				}
				return sandbox.SurveyContinue, err
			}
			cost.entries++
			if info.Mode().IsRegular() {
				cost.bytes += max(info.Size(), 0)
			}
			return sandbox.SurveyContinue, nil
		})
	if err != nil {
		return sourceCost{}, err
	}
	progress.Stage = StageCostDeclared
	progress.TotalEntries, progress.TotalBytes = cost.entries, cost.bytes
	reportPreparation(ctx, progress)
	available, known, err := queryAvailableStorageBytes(branchRoot)
	if err != nil {
		return sourceCost{}, err
	}
	if known && available < uint64(cost.bytes) {
		return sourceCost{}, &CapacityError{Path: branchRoot, Required: uint64(cost.bytes), Available: available}
	}
	return cost, nil
}
