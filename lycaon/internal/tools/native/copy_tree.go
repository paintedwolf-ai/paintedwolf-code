package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// copyTree preflights without retaining a tree-sized plan, then routes every
// entry through the same mutation owners as a single-file copy. The second
// pass measures actual streamed bytes so growth cannot exceed admission.
func (op *CopyOperation) copyTree(ctx context.Context, tc tools.ToolContext, source projectpaths.Resolved, pair FilePair, result *CopyResult) error {
	t, in, caps, used, memory := op.tool, op.input, op.caps, &op.used, op.memory
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
	files, bytes := used.Files, used.Bytes
	inspect := func(rel string, info os.FileInfo) error {
		if _, err := assertCopyWritePath(ctx, t.Boundary, tc, filepath.Join(pair.To, rel), tc.ProfileID()); err != nil {
			return err
		}
		if info.IsDir() {
			_, err := assertCopyWritePath(ctx, t.Boundary, tc, filepath.Join(pair.From, rel), tc.ProfileID())
			return err
		}
		from, err := assertMutationFileSource(ctx, t.Boundary, tc, filepath.Join(pair.From, rel), tc.ProfileID(), "copy", "COPY_NOT_FOUND", "COPY_IS_DIRECTORY", "COPY_SYMLINK_ESCAPE", assertCopyWritePath)
		if err != nil {
			return err
		}
		held, err := op.reads.Open(from)
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
	if err := walkMutationTree(ctx, root, base, ".", inspect, nil, memory); err != nil {
		return err
	}
	created, err := mkdirAgentPath(ctx, tc, resolvedMutationTarget(destination), 0o755)
	result.Effects += created
	if err != nil {
		return err
	}
	page, err := newCopyPage(ctx, tc, op, result)
	if err != nil {
		return err
	}
	defer page.Close()
	copyEntry := func(rel string, info os.FileInfo) error {
		to := filepath.Join(pair.To, rel)
		if info.IsDir() {
			if err := page.Flush(); err != nil {
				return err
			}
			target, err := assertCopyWritePath(ctx, t.Boundary, tc, to, tc.ProfileID())
			if err != nil {
				return err
			}
			created, err := mkdirAgentPath(ctx, tc, resolvedMutationTarget(target), 0o755)
			result.Effects += created
			return err
		}
		used.Files++
		if err := copyTreeWithinCaps(used.Files, used.Bytes, caps, filepath.ToSlash(pair.From)); err != nil {
			return err
		}
		remaining := caps.Bytes - used.Bytes - page.bytes
		if in.MaxFileBytes > 0 {
			remaining = min(remaining, in.MaxFileBytes)
		}
		return page.Add(FilePair{From: filepath.Join(pair.From, rel), To: to}, remaining)
	}
	finishDir := func(rel string, _ os.FileInfo) error {
		if err := page.Flush(); err != nil {
			return err
		}
		if !in.Preserve {
			return nil
		}
		target, err := assertCopyWritePath(ctx, t.Boundary, tc, filepath.Join(pair.To, rel), tc.ProfileID())
		if err != nil {
			return err
		}
		return preserveMetadata(ctx, tc, filepath.Join(pair.From, rel), resolvedMutationTarget(target))
	}
	err = walkMutationTree(ctx, root, base, ".", copyEntry, finishDir, memory)
	return errors.Join(err, page.Flush())
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
