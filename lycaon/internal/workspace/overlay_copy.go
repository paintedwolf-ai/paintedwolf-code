package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
)

var workspaceMirrorConcurrency = min(max(runtime.GOMAXPROCS(0), 4), 16)
var cloneWorkspaceFile = fileclone.Clone

type overlayCopyJob struct {
	src, dst, rel string
	mode          fs.FileMode
	modTime       time.Time
}

type directoryMetadata struct {
	src, dst string
	mode     fs.FileMode
	modTime  time.Time
}

type mirrorOptions struct {
	phase                string
	tolerateChange       bool
	fileSource           func(rel, sourcePath string) string
	preparation          PreparationProgress
	maxEntries, maxBytes int64
}

type mirrorResult struct {
	Files, Directories, Symlinks, Bytes   int64
	ClonedFiles, ClonedBytes, CopiedBytes int64
	SkippedPaths                          []string
}

func validateCompleteMirror(result mirrorResult) error {
	if len(result.SkippedPaths) == 0 {
		return nil
	}
	return fmt.Errorf("source changed during snapshot: %d paths skipped", len(result.SkippedPaths))
}

// MaterializeBoundedWorkspace prepares an isolated materialized worker view using adapter.EagerCopyAdapter,
// enforcing entry and byte limits (returning *adapter.PreparationLimitError with workspace_preparation_limit),
// narrowing roots via WorkspaceSelection while keeping TaskPathHints separate, and isolating .git/gitlinks (#1658).
func MaterializeBoundedWorkspace(
	ctx context.Context, viewID, runtimeDir string, grants []adapter.RootGrant,
	selection adapter.WorkspaceSelection, taskPathHints []string,
	maxEntries, maxBytes int64, fallbackReason adapter.ReasonCode,
) (*adapter.PreparedView, error) {
	adp := adapter.NewEagerCopyAdapter(fallbackReason)
	return adp.Prepare(ctx, adapter.PrepareRequest{
		ViewID: viewID, RuntimeDir: runtimeDir, Grants: grants, Selection: selection,
		TaskPathHints: taskPathHints, MaxEntries: maxEntries, MaxBytes: maxBytes,
		ProgressFn: func(fp adapter.FallbackProgress) {
			reportPreparation(ctx, PreparationProgress{
				Strategy: ProvisionMaterializedEagerCopy, Stage: "snapshotting_branch",
				Files: fp.ObservedEntries, Bytes: fp.ObservedBytes, TotalBytes: fp.MaxBytes,
			})
		},
	})
}

// CreateWorkerWorkspace creates a complete single-root branch.
func (m *Manager) CreateWorkerWorkspace(ctx context.Context, primaryDir, jobID string) (*Binding, error) {
	primary, err := absDir(primaryDir)
	if err != nil {
		return nil, err
	}
	roots := []projectroot.RootRef{{ID: "primary", Path: primary, IsPrimary: true}}
	binding, _, err := m.CreateWorkerBranch(ctx, roots, "primary", jobID)
	return binding, err
}

// DestroyWorkerWorkspace releases a branch's views, proves no mount remains in it, drops its retention
// and deletes it. Any failure keeps the branch and its metadata: the view is still in custody, and
// recursive removal through a live mount would delete the worker's result.
func (m *Manager) DestroyWorkerWorkspace(binding *Binding) error {
	if binding == nil || strings.TrimSpace(binding.Root) == "" {
		return nil
	}
	root := strings.TrimSpace(binding.Root)
	jobID := jobIDForBranch(root)
	if err := m.releaseBranchRuntime(binding, root, jobID); err != nil {
		return err
	}
	if err := RefuseMountedBranch(root); err != nil {
		return err
	}
	if err := m.releaseBranchRetention(jobID); err != nil {
		return err
	}
	releaseBranchClaimState(root)
	err := errors.Join(os.RemoveAll(enginepaths.MetaDirForBranchRoot(root)), os.RemoveAll(root))
	removeBranchLocks(root)
	return err
}

func (m *Manager) releaseBranchRuntime(binding *Binding, root, jobID string) error {
	mounted, err := m.settleMountedBranch(root, jobID)
	if err != nil {
		return err
	}
	if mounted {
		m.forgetBranchViews(root)
		return nil
	}
	settled, err := m.releaseBranchViews(root)
	if err != nil {
		return err
	}
	if binding.Adapter == nil || binding.View == nil || settled[binding.View.ViewID] {
		return nil
	}
	return releaseAdapterView(binding.Adapter, binding.View.ViewID)
}

// releaseBranchRetention drops what recovery retained for a worker whose branch is going away, for a
// result promoted or discarded. Collection removes only what nothing else still references.
func (m *Manager) releaseBranchRetention(jobID string) error {
	if strings.TrimSpace(m.branchRoot) == "" {
		return nil
	}
	services, err := m.layered()
	if err != nil {
		return err
	}
	return services.recovery.ReleaseWorkerResult(jobID)
}

// settleMountedBranch releases the branch's mounted view when it has one. On failure the caller must
// keep the branch and its metadata: the view is still in custody, and the directory may still be a mount.
func (m *Manager) settleMountedBranch(branchRoot, jobID string) (bool, error) {
	branchClaimsMu.RLock()
	rec, ok := branchClaimStates[filepath.Clean(branchRoot)]
	branchClaimsMu.RUnlock()
	if ok && rec.layered != nil {
		return true, settleBranchLayeredView(context.Background(), branchRoot, rec.layered)
	}
	if strings.TrimSpace(m.branchRoot) == "" {
		return false, nil
	}
	services, err := m.layered()
	if err != nil {
		return false, err
	}
	if !services.coordinator.SettlementUnresolved(jobID) {
		return false, nil
	}
	return true, services.coordinator.ReleaseWorkerView(context.Background(), jobID, services.retention)
}

func markSnapshotCompleteWithClaim(branchRoot string, choice WorkspaceStrategySelection, view *adapter.PreparedView, cv *claim.ClaimedView, hints []string) error {
	metaDir := enginepaths.MetaDirForBranchRoot(branchRoot)
	meta, err := LoadJobMeta(metaDir)
	if err != nil {
		return err
	}
	cpFacts := choice.Facts
	if view != nil && view.Facts.AdapterID != "" {
		cpFacts = view.Facts
	}
	meta.FormatVersion, meta.SnapshotComplete = JobMetaFormatV2, true
	meta.AdapterID, meta.PreparationKind = choice.AdapterID, choice.Facts.PreparationKind
	meta.VisibilityMode, meta.FallbackReason = choice.Facts.UntouchedSourceVisibility, choice.Facts.FallbackReason
	meta.PendingGitInit = choice.Facts.PreparationKind == adapter.PreparationMounted
	meta.SourceGeneration, meta.FocusPathHints, meta.AdapterFacts = 1, append([]string(nil), hints...), &cpFacts
	if view != nil {
		meta.ViewID, meta.PreparedViewID = view.ViewID, view.ViewID
		meta.CopiedEntries, meta.CopiedBytes = view.ObservedEntries, view.ObservedBytes
		if view.BackingDir != "" {
			meta.JournalPath = filepath.Join(view.BackingDir, "journal")
		}
	}
	if cv != nil {
		meta.JournalPath = cv.Metadata.JournalPath
	} else {
		recordBranchClaimState(branchRoot, nil, snapshotFromJobMeta(branchRoot, meta), nil)
	}
	return WriteJobMeta(metaDir, meta)
}

func snapshotFromJobMeta(branchRoot string, meta JobMeta) claim.WorkspaceStateSnapshot {
	roots := make([]claim.RootMappingRecord, 0, len(meta.Roots))
	for _, r := range meta.Roots {
		roots = append(roots, claim.RootMappingRecord{
			RootID: r.ID, Label: r.Label, SourcePath: r.Path, WorkspacePath: branchRoot, IsPrimary: r.IsPrimary,
		})
	}
	gitState := claim.GitInitInitialized
	if meta.PendingGitInit {
		gitState = claim.GitInitLazyPending
	}
	notice := claim.FallbackNotice{Severity: claim.SeverityInfo}
	if meta.FallbackReason != "" {
		notice = claim.FallbackNotice{
			Severity: claim.SeverityWarning, ReasonCode: meta.FallbackReason,
			CopiedEntries: meta.CopiedEntries, CopiedBytes: meta.CopiedBytes,
		}
	}
	return claim.WorkspaceStateSnapshot{
		Identity:       claim.ClaimIdentity{WorkerID: jobIDForBranch(branchRoot), ViewID: meta.ViewID, SourceGeneration: uint64(max(meta.SourceGeneration, 0))},
		Readiness:      claim.StateReady,
		Adapter:        claim.AdapterMetadataFacts{AdapterID: meta.AdapterID, PreparationKind: meta.PreparationKind, SourceVisibility: meta.VisibilityMode, FallbackReason: meta.FallbackReason, CopiedEntries: meta.CopiedEntries, CopiedBytes: meta.CopiedBytes, PendingGitInit: meta.PendingGitInit},
		SelectedRoots:  roots,
		FocusPathHints: append([]string(nil), meta.FocusPathHints...),
		Notice:         notice,
		GitInit:        gitState,
		Command:        claim.CommandIdle,
		Checkpoint:     claim.CheckpointStatus{State: claim.CheckpointNone},
	}
}

func jobRoot(m *Manager, primaryDir, jobID string) string {
	return enginepaths.JobBranchDir(m.branchRoot, primaryDir, jobID)
}

func mirrorTree(ctx context.Context, srcRoot, dstRoot string, opts mirrorOptions) (mirrorResult, error) {
	if err := os.MkdirAll(dstRoot, 0o750); err != nil {
		return mirrorResult{}, err
	}
	workers := max(workspaceMirrorConcurrency, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan overlayCopyJob, workers*2)
	var (
		wg                                       sync.WaitGroup
		errOnce                                  sync.Once
		firstErr                                 error
		entries, walkBytes, files, bytes, clones atomic.Int64
		cloneB, copyB, dirs, links               atomic.Int64
		skipMu                                   sync.Mutex
		skipped                                  []string
	)
	fail := func(err error) { errOnce.Do(func() { firstErr = err; cancel() }) }
	recordSkip := func(rel string) { skipMu.Lock(); skipped = append(skipped, rel); skipMu.Unlock() }
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					fail(fmt.Errorf("workspace mirror worker panicked: %v", recovered))
				}
			}()
			for job := range jobs {
				cloned, err := cloneOrCopyFile(ctx, job.src, job.dst, job.mode, job.modTime)
				if err != nil {
					if opts.tolerateChange && sourceChangeSkippable(err) {
						recordSkip(job.rel)
						continue
					}
					fail(fmt.Errorf("mirror %s: %w", job.rel, err))
					return
				}
				files.Add(1)
				info, statErr := os.Stat(job.dst)
				if statErr == nil {
					curBytes := bytes.Add(info.Size())
					if limitErr := checkPreparationLimits(entries.Load(), curBytes, opts.maxEntries, opts.maxBytes, true); limitErr != nil {
						fail(limitErr)
						return
					}
					if cloned {
						clones.Add(1)
						cloneB.Add(info.Size())
					} else {
						copyB.Add(info.Size())
					}
					progress := opts.preparation
					progress.Files = files.Load()
					progress.Bytes = curBytes
					reportPreparation(ctx, progress)
				}
			}
		}()
	}

	progressDone := make(chan struct{})
	go logMirrorProgress(ctx, progressDone, opts.phase, srcRoot, &files, &bytes)
	var directoryQueue []directoryMetadata
	walkErr := sandbox.SurveyWalk(ctx, srcRoot, sandbox.SurveyOptions{IncludeHidden: true},
		func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			dst := filepath.Join(dstRoot, filepath.FromSlash(entry.Rel))
			if existing, statErr := os.Lstat(dst); statErr == nil {
				if existing.Mode().IsRegular() || existing.IsDir() || existing.Mode()&os.ModeSymlink != 0 {
					return sandbox.SurveyContinue, nil
				}
			} else if !os.IsNotExist(statErr) {
				return sandbox.SurveyContinue, statErr
			}
			info, err := entry.DirEntry.Info()
			if err != nil {
				if opts.tolerateChange && sourceChangeSkippable(err) {
					recordSkip(entry.Rel)
					return sandbox.SurveyContinue, nil
				}
				return sandbox.SurveyContinue, err
			}
			curEntries := entries.Add(1)
			curBytes := walkBytes.Load()
			if info.Mode().IsRegular() && info.Size() > 0 {
				curBytes = walkBytes.Add(info.Size())
			}
			if limitErr := checkPreparationLimits(curEntries, curBytes, opts.maxEntries, opts.maxBytes, true); limitErr != nil {
				fail(limitErr)
				return sandbox.SurveyStop, limitErr
			}
			switch {
			case entry.IsDir:
				if err := os.MkdirAll(dst, 0o750); err != nil {
					return sandbox.SurveyContinue, err
				}
				directoryQueue = append(directoryQueue, directoryMetadata{
					src: entry.Abs, dst: dst, mode: info.Mode(), modTime: info.ModTime(),
				})
				dirs.Add(1)
			case entry.IsSymlink:
				if err := mirrorSymlink(entry.Abs, dst); err != nil {
					if opts.tolerateChange && sourceChangeSkippable(err) {
						recordSkip(entry.Rel)
						return sandbox.SurveyContinue, nil
					}
					return sandbox.SurveyContinue, err
				}
				links.Add(1)
			case info.Mode().IsRegular():
				src := entry.Abs
				if opts.fileSource != nil {
					src = opts.fileSource(entry.Rel, src)
				}
				select {
				case <-ctx.Done():
					return sandbox.SurveyStop, ctx.Err()
				case jobs <- overlayCopyJob{
					src: src, dst: dst, rel: entry.Rel, mode: info.Mode(), modTime: info.ModTime(),
				}:
				}
			}
			return sandbox.SurveyContinue, nil
		})
	close(jobs)
	wg.Wait()
	close(progressDone)
	if firstErr != nil {
		_ = os.RemoveAll(dstRoot)
		return mirrorResult{}, markIncompleteSeedCleaned(firstErr)
	}
	if walkErr != nil {
		_ = os.RemoveAll(dstRoot)
		return mirrorResult{}, markIncompleteSeedCleaned(walkErr)
	}
	if err := finalizeDirectoryMetadata(directoryQueue); err != nil {
		return mirrorResult{}, err
	}
	result := mirrorResult{
		Files: files.Load(), Directories: dirs.Load(), Symlinks: links.Load(), Bytes: bytes.Load(),
		ClonedFiles: clones.Load(), ClonedBytes: cloneB.Load(), CopiedBytes: copyB.Load(),
		SkippedPaths: append([]string(nil), skipped...),
	}
	if len(result.SkippedPaths) > 0 {
		slog.WarnContext(ctx, "source entries changed during workspace mirror",
			"phase", opts.phase, "source", srcRoot, "skipped", len(result.SkippedPaths))
	}
	return result, ctx.Err()
}
