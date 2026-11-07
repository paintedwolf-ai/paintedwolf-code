package workspace

import (
	"context"
	"errors"
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
	admit          sourceAdmit
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
	// UnreadableEntries could not be read as this user. The worker could not read
	// them either, so they are absent from its tree rather than a failed claim.
	UnreadableEntries int64
}

func validateCompleteMirror(result mirrorResult) error {
	if len(result.SkippedPaths) == 0 {
		return nil
	}
	return fmt.Errorf("source changed during snapshot: %d paths skipped", len(result.SkippedPaths))
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

// DestroyWorkerWorkspace removes a branch and its metadata. A branch on an absent
// volume keeps its record, so it can be removed when the volume returns.
func (m *Manager) DestroyWorkerWorkspace(ctx context.Context, binding *Binding) error {
	if binding == nil {
		return nil
	}
	root := strings.TrimSpace(binding.Root)
	if root == "" {
		return nil
	}
	if err := branchVolumeAvailable(root); err != nil {
		return err
	}
	_ = os.RemoveAll(enginepaths.MetaDirForBranchRoot(root))
	removeBranchLocks(root)
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	return forgetPlacedBranch(ctx, m.stateRoot, root)
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
		wg         sync.WaitGroup
		errOnce    sync.Once
		firstErr   error
		files      atomic.Int64
		bytes      atomic.Int64
		clones     atomic.Int64
		cloneB     atomic.Int64
		copyB      atomic.Int64
		dirs       atomic.Int64
		links      atomic.Int64
		unreadable atomic.Int64
		skipMu     sync.Mutex
		skipped    []string
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
					if errors.Is(err, fs.ErrPermission) && sourceUnreadable(job.src) {
						unreadable.Add(1)
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
	walkErr := sandbox.SurveyWalk(ctx, srcRoot, sandbox.SurveyOptions{IncludeHidden: true, Admit: opts.admit, OnReadError: skipUnreadable(&unreadable)},
		func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			dst := filepath.Join(dstRoot, filepath.FromSlash(entry.Rel))
			// Leave nodes already present on the destination.
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
				// A clone can succeed without read permission and leave a file the
				// baseline capture cannot open.
				if info.Mode().Perm()&0o400 == 0 {
					unreadable.Add(1)
					return sandbox.SurveyContinue, nil
				}
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
	if err := finalizeDirectoryMetadata(ctx, directoryQueue); err != nil {
		return mirrorResult{}, err
	}
	result := mirrorResult{
		Files: files.Load(), Directories: dirs.Load(), Symlinks: links.Load(), Bytes: bytes.Load(),
		ClonedFiles: clones.Load(), ClonedBytes: cloneB.Load(), CopiedBytes: copyB.Load(),
		SkippedPaths: append([]string(nil), skipped...), UnreadableEntries: unreadable.Load(),
	}
	if len(result.SkippedPaths) > 0 {
		slog.WarnContext(ctx, "source entries changed during workspace mirror",
			"phase", opts.phase, "source", srcRoot, "skipped", len(result.SkippedPaths))
	}
	return result, ctx.Err()
}

func logMirrorProgress(
	ctx context.Context,
	done <-chan struct{},
	phase, source string,
	files, bytes *atomic.Int64,
) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			slog.InfoContext(ctx, "preparing isolated worker workspace",
				"phase", phase, "source", source, "files", files.Load(), "bytes", bytes.Load())
		}
	}
}

func finalizeDirectoryMetadata(ctx context.Context, queue []directoryMetadata) error {
	for i := len(queue) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := queue[i]
		// A directory that could not be listed stays an empty, readable one here.
		if entry.mode.Perm()&0o500 != 0o500 {
			continue
		}
		if err := os.Chmod(entry.dst, entry.mode.Perm()); err != nil {
			return err
		}
		if err := copyExtendedMetadata(entry.src, entry.dst); err != nil {
			return err
		}
		if err := os.Chtimes(entry.dst, entry.modTime, entry.modTime); err != nil {
			return err
		}
	}
	return nil
}

func sourceChangeSkippable(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

func mirrorSymlink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if err := os.Symlink(target, dst); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
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
