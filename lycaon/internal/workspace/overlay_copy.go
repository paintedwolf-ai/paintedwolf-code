package workspace

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
)

var workspaceMirrorConcurrency = min(max(runtime.GOMAXPROCS(0), 4), 16)
var cloneWorkspaceFile = fileclone.Clone

type overlayCopyJob struct {
	src     string
	dst     string
	rel     string
	mode    fs.FileMode
	modTime time.Time
}

type directoryMetadata struct {
	src     string
	dst     string
	mode    fs.FileMode
	modTime time.Time
}

type mirrorOptions struct {
	phase          string
	tolerateChange bool
	fileSource     func(rel, sourcePath string) string
	preparation    PreparationProgress
}

type mirrorResult struct {
	Files        int64
	Directories  int64
	Symlinks     int64
	Bytes        int64
	ClonedFiles  int64
	ClonedBytes  int64
	CopiedBytes  int64
	SkippedPaths []string
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
	ctx context.Context,
	viewID string,
	runtimeDir string,
	grants []adapter.RootGrant,
	selection adapter.WorkspaceSelection,
	taskPathHints []string,
	maxEntries int64,
	maxBytes int64,
	fallbackReason adapter.ReasonCode,
) (*adapter.PreparedView, error) {
	adp := adapter.NewEagerCopyAdapter(fallbackReason)
	return adp.Prepare(ctx, adapter.PrepareRequest{
		ViewID:        viewID,
		RuntimeDir:    runtimeDir,
		Grants:        grants,
		Selection:     selection,
		TaskPathHints: taskPathHints,
		MaxEntries:    maxEntries,
		MaxBytes:      maxBytes,
		ProgressFn: func(fp adapter.FallbackProgress) {
			reportPreparation(ctx, PreparationProgress{
				Strategy:   ProvisionMaterializedEagerCopy,
				Stage:      "snapshotting_branch",
				Files:      fp.ObservedEntries,
				Bytes:      fp.ObservedBytes,
				TotalBytes: fp.MaxBytes,
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
	roots := []projectroot.RootRef{
		{ID: "primary", Path: primary, IsPrimary: true},
	}
	binding, _, err := m.CreateWorkerWorkspaceFromSources(ctx, roots, roots, "primary", jobID)
	return binding, err
}

// DestroyWorkerWorkspace removes a branch and its metadata.
func (m *Manager) DestroyWorkerWorkspace(binding *Binding) error {
	if binding == nil {
		return nil
	}
	root := strings.TrimSpace(binding.Root)
	if root == "" {
		return nil
	}
	_ = os.RemoveAll(enginepaths.MetaDirForBranchRoot(root))
	removeBranchLocks(root)
	return os.RemoveAll(root)
}

func (m *Manager) jobRoot(primaryDir, jobID string) string {
	return enginepaths.JobBranchDir(m.branchRoot, primaryDir, jobID)
}

func mirrorTree(ctx context.Context, srcRoot, dstRoot string, opts mirrorOptions) (mirrorResult, error) {
	if err := os.MkdirAll(dstRoot, 0o750); err != nil {
		return mirrorResult{}, err
	}
	workers := workspaceMirrorConcurrency
	if workers < 1 {
		workers = 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan overlayCopyJob, workers*2)
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
		files    atomic.Int64
		bytes    atomic.Int64
		clones   atomic.Int64
		cloneB   atomic.Int64
		copyB    atomic.Int64
		dirs     atomic.Int64
		links    atomic.Int64
		skipMu   sync.Mutex
		skipped  []string
	)
	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}
	recordSkip := func(rel string) {
		skipMu.Lock()
		skipped = append(skipped, rel)
		skipMu.Unlock()
	}
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
					bytes.Add(info.Size())
					if cloned {
						clones.Add(1)
						cloneB.Add(info.Size())
					} else {
						copyB.Add(info.Size())
					}
					progress := opts.preparation
					progress.Files = files.Load()
					progress.Bytes = bytes.Load()
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
		return mirrorResult{}, firstErr
	}
	if walkErr != nil {
		return mirrorResult{}, walkErr
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

func cloneOrCopyFile(ctx context.Context, src, dst string, mode fs.FileMode, modTime time.Time) (bool, error) {
	cloned, err := cloneWorkspaceFile(src, dst)
	if err != nil {
		return false, err
	}
	if !cloned {
		if err := copyRegularFile(ctx, src, dst, mode); err != nil {
			return false, err
		}
	}
	if err := copyExtendedMetadata(src, dst); err != nil {
		_ = os.Remove(dst)
		return false, err
	}
	if err := os.Chmod(dst, mode.Perm()); err != nil {
		_ = os.Remove(dst)
		return false, err
	}
	if err := os.Chtimes(dst, modTime, modTime); err != nil {
		_ = os.Remove(dst)
		return false, err
	}
	return cloned, nil
}

func copyRegularFile(ctx context.Context, src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, contextio.Reader{Context: ctx, Source: in})
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	return nil
}
