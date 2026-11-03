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
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// OverlaySurveyReadDir lists one branch directory.
func OverlaySurveyReadDir(branchRoot, rel string, opts sandbox.SurveyOptions) ([]sandbox.SurveyEntry, error) {
	branchRoot = strings.TrimSpace(branchRoot)
	if branchRoot == "" || !filepath.IsAbs(branchRoot) {
		return nil, fmt.Errorf("absolute branch root required")
	}
	branchRoot = filepath.Clean(branchRoot)
	rel = strings.TrimSpace(rel)
	if filepath.IsAbs(filepath.FromSlash(rel)) || sandbox.HasParentTraversal(rel) {
		return nil, fmt.Errorf("invalid branch path %q", rel)
	}
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	branchDir := branchRoot
	if rel != "" && rel != "." {
		branchDir = filepath.Join(branchRoot, filepath.FromSlash(rel))
	}
	entries, err := os.ReadDir(branchDir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	out := make([]sandbox.SurveyEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !opts.IncludeHidden && sandbox.IsHiddenName(name) {
			continue
		}
		childRel := name
		if rel != "" && rel != "." {
			childRel = rel + "/" + name
		}
		if e.IsDir() && surveySkipsOverlayDir(name, opts) {
			continue
		}
		abs := filepath.Join(branchDir, name)
		out = append(out, sandbox.SurveyEntry{
			Rel:       childRel,
			Abs:       abs,
			Depth:     strings.Count(childRel, "/") + 1,
			IsDir:     e.IsDir(),
			IsSymlink: e.Type()&fs.ModeSymlink != 0,
			DirEntry:  e,
		})
	}
	return out, nil
}

// OverlaySurveyWalk walks a branch.
func OverlaySurveyWalk(
	ctx context.Context,
	branchRoot string,
	opts sandbox.SurveyOptions,
	fn func(sandbox.SurveyEntry) (sandbox.SurveyAction, error),
) error {
	branchRoot = filepath.Clean(strings.TrimSpace(branchRoot))
	var walk func(rel string) error
	walk = func(rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := OverlaySurveyReadDir(branchRoot, rel, opts)
		if err != nil {
			if os.IsNotExist(err) && rel != "." {
				return nil
			}
			return err
		}
		for _, entry := range entries {
			if opts.Admit != nil && !opts.Admit(entry.Rel, entry.Abs, entry.IsDir) {
				continue
			}
			if opts.MaxDepth > 0 && entry.Depth > opts.MaxDepth {
				continue
			}
			action, err := fn(entry)
			if err != nil {
				return err
			}
			switch action {
			case sandbox.SurveyStop:
				return nil
			case sandbox.SurveySkipDir:
				continue
			case sandbox.SurveyContinue:
			}
			if entry.IsDir && !entry.IsSymlink {
				if opts.MaxDepth > 0 && entry.Depth >= opts.MaxDepth {
					continue
				}
				if err := walk(entry.Rel); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(".")
}

func surveySkipsOverlayDir(name string, opts sandbox.SurveyOptions) bool {
	if !opts.IncludeVCSMetadata && sandbox.IsVCSDirBaseName(name) {
		return true
	}
	if !opts.IncludeEngineOverlay && sandbox.IsEngineOverlayBaseName(name) {
		return true
	}
	return false
}

func checkPreparationLimits(observedEntries, observedBytes, maxEntries, maxBytes int64, cleaned bool) error {
	if (maxEntries > 0 && observedEntries > maxEntries) || (maxBytes > 0 && observedBytes > maxBytes) {
		return &adapter.PreparationLimitError{
			ReasonCode:            adapter.ReasonWorkspacePreparationLimit,
			ObservedEntries:       observedEntries,
			MaxEntries:            maxEntries,
			ObservedBytes:         observedBytes,
			MaxBytes:              maxBytes,
			CleanedIncompleteSeed: cleaned,
		}
	}
	return nil
}

func markIncompleteSeedCleaned(err error) error {
	var limitErr *adapter.PreparationLimitError
	if errors.As(err, &limitErr) {
		limitErr.CleanedIncompleteSeed = true
		return limitErr
	}
	return err
}

func regularBytes(manifest seedManifest) int64 {
	var total int64
	for _, entry := range manifest.Entries {
		if entry.Kind == "regular" && entry.Size > 0 {
			total += entry.Size
		}
	}
	return total
}

func changedRegularBytes(next, current seedManifest) uint64 {
	var total uint64
	for rel, entry := range next.Entries {
		if entry.Kind != "regular" || entry.Size <= 0 {
			continue
		}
		if old, ok := current.Entries[rel]; ok && old == entry {
			continue
		}
		total += uint64(entry.Size)
	}
	return total
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

func finalizeDirectoryMetadata(queue []directoryMetadata) error {
	for i := len(queue) - 1; i >= 0; i-- {
		entry := queue[i]
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
