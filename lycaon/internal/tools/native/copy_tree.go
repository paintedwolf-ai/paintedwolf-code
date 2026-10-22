package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// copyTree preflights without retaining a tree-sized plan, then routes every
// entry through the same mutation owners as a single-file copy. The second
// pass measures actual streamed bytes so growth cannot exceed admission.
func (t *CopyTool) copyTree(ctx context.Context, tc tools.ToolContext, source projectpaths.Resolved, pair FilePair, in CopyInput, caps RecursiveCaps, result *CopyResult) error {
	destination, err := assertCopyWritePath(ctx, t.Boundary, tc, pair.To, tc.ProfileID())
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(source.Abs, destination.Abs); err == nil && filepath.IsLocal(rel) {
		return fmt.Errorf("copy %s: destination is inside the source directory", filepath.ToSlash(pair.To))
	}
	root, err := fseffect.OpenReadRoot(source.EffectLocation().Root)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	base := source.EffectLocation().Rel
	var files, bytes int64
	inspect := func(rel string, info os.FileInfo) error {
		if _, err := assertCopyWritePath(ctx, t.Boundary, tc, filepath.Join(pair.To, rel), tc.ProfileID()); err != nil {
			return err
		}
		if _, err := assertCopyWritePath(ctx, t.Boundary, tc, filepath.Join(pair.From, rel), tc.ProfileID()); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		from, err := assertMutationFileSource(ctx, t.Boundary, tc, filepath.Join(pair.From, rel), tc.ProfileID(), "copy", "COPY_NOT_FOUND", "COPY_IS_DIRECTORY", "COPY_SYMLINK_ESCAPE", assertCopyWritePath)
		if err != nil {
			return err
		}
		held, err := fseffect.OpenRead(from.EffectLocation())
		if err != nil {
			return err
		}
		observed, statErr := held.Stat()
		closeErr := held.Close()
		if err := errors.Join(statErr, closeErr); err != nil {
			return err
		}
		files++
		bytes += observed.Size()
		return copyTreeWithinCaps(files, bytes, caps, filepath.ToSlash(pair.From))
	}
	if err := walkCopyTree(ctx, root, base, ".", inspect, nil); err != nil {
		return err
	}
	if err := mkdirAgentPath(ctx, tc, resolvedMutationTarget(destination), 0o755); err != nil {
		return err
	}
	files, bytes = 0, 0
	copyEntry := func(rel string, info os.FileInfo) error {
		to := filepath.Join(pair.To, rel)
		if info.IsDir() {
			target, err := assertCopyWritePath(ctx, t.Boundary, tc, to, tc.ProfileID())
			if err != nil {
				return err
			}
			return mkdirAgentPath(ctx, tc, resolvedMutationTarget(target), 0o755)
		}
		files++
		if err := copyTreeWithinCaps(files, bytes, caps, filepath.ToSlash(pair.From)); err != nil {
			return err
		}
		remaining := caps.Bytes - bytes
		if in.MaxFileBytes > 0 {
			remaining = min(remaining, in.MaxFileBytes)
		}
		before := len(result.Copied)
		if err := t.copyFile(ctx, tc, FilePair{From: filepath.Join(pair.From, rel), To: to}, in, remaining, result); err != nil {
			return err
		}
		if len(result.Copied) > before {
			bytes += result.Copied[len(result.Copied)-1].Bytes
		}
		return nil
	}
	finishDir := func(rel string, _ os.FileInfo) error {
		if !in.Preserve {
			return nil
		}
		target, err := assertCopyWritePath(ctx, t.Boundary, tc, filepath.Join(pair.To, rel), tc.ProfileID())
		if err != nil {
			return err
		}
		return preserveMetadata(ctx, tc, filepath.Join(pair.From, rel), resolvedMutationTarget(target))
	}
	return walkCopyTree(ctx, root, base, ".", copyEntry, finishDir)
}

func copyTreeWithinCaps(files, bytes int64, caps RecursiveCaps, path string) error {
	if files > caps.Files {
		return recursiveCapRefusal("recursive_mutation_files", "files", caps.Files, files, path)
	}
	if bytes > caps.Bytes {
		return recursiveCapRefusal("recursive_mutation_bytes", "bytes", caps.Bytes, bytes, path)
	}
	return nil
}

// Each active directory retains at most one page; empty directories do not
// accumulate in a plan. Descriptor-relative reads keep traversal in its root.
func walkCopyTree(ctx context.Context, root *fseffect.ReadRoot, base, rel string, visit, after func(string, os.FileInfo) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := root.Open(filepath.Join(base, rel))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("copy %s: source directory changed", rel)
	}
	if err := visit(rel, info); err != nil {
		return err
	}
	for {
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			child := filepath.Join(rel, entry.Name())
			if info.IsDir() {
				if err := walkCopyTree(ctx, root, base, child, visit, after); err != nil {
					return err
				}
			} else if err := visit(child, info); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if after != nil {
		return after(rel, info)
	}
	return nil
}
