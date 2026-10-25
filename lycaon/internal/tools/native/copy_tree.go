package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func canFastResolveCopyTree(ctx context.Context, b *sandbox.Boundary, tc tools.ToolContext, source, destination projectpaths.Resolved) bool {
	if source.External || destination.External {
		return false
	}
	if tc.ProfileID() == coordinatorProfileID {
		return false
	}
	if _, pinned := sandbox.TurnWritePinFromContext(ctx); pinned {
		return false
	}
	if b != nil && len(b.WriteGlobsForProfile(ctx, tc.ProfileID())) > 0 {
		return false
	}
	return true
}

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
	_, destStatErr := os.Lstat(destination.Abs)
	destExisted := !os.IsNotExist(destStatErr)
	fastResolve := canFastResolveCopyTree(ctx, t.Boundary, tc, source, destination)
	root, err := fseffect.OpenReadRoot(source.EffectLocation().Root)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	base := source.EffectLocation().Rel
	files, bytes := used.Files, used.Bytes
	inspect := func(rel string, info os.FileInfo) error {
		if info.IsDir() {
			toResolved, err := assertCopyWritePath(ctx, t.Boundary, tc, filepath.Join(pair.To, rel), tc.ProfileID())
			if err != nil {
				return err
			}
			if destExisted && fastResolve {
				if dstInfo, statErr := os.Lstat(toResolved.Abs); statErr == nil && !dstInfo.IsDir() {
					fastResolve = false
				}
			}
			_, err = assertCopyWritePath(ctx, t.Boundary, tc, filepath.Join(pair.From, rel), tc.ProfileID())
			return err
		}
		if fastResolve && info.Mode().IsRegular() {
			fromChild := projectpaths.ChildResolved(tc, source, rel)
			toChild := projectpaths.ChildResolved(tc, destination, rel)
			if !projectpaths.IsSpecialMutationRel(fromChild.ScopeRel) && !projectpaths.IsSpecialMutationRel(toChild.ScopeRel) {
				files++
				bytes += info.Size()
				if err := copyTreeWithinCaps(files, bytes, caps, filepath.ToSlash(pair.From)); err != nil {
					projectpaths.RecordResolvedMutation(ctx, tc, toChild)
					projectpaths.RecordResolvedMutation(ctx, tc, fromChild)
					return err
				}
				return nil
			}
		}
		if _, err := assertCopyWritePath(ctx, t.Boundary, tc, filepath.Join(pair.To, rel), tc.ProfileID()); err != nil {
			return err
		}
		from, err := assertMutationFileSource(ctx, t.Boundary, tc, filepath.Join(pair.From, rel), tc.ProfileID(), "copy", "COPY_NOT_FOUND", "COPY_IS_DIRECTORY", "COPY_SYMLINK_ESCAPE", assertCopyWritePath)
		if err != nil {
			return err
		}
		observed := info
		if !info.Mode().IsRegular() {
			held, err := op.reads.Open(ctx, from)
			if err != nil {
				return err
			}
			current, statErr := held.Stat()
			closeErr := held.Close()
			if err := errors.Join(statErr, closeErr); err != nil {
				return err
			}
			observed = current
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
			if fastResolve {
				toChild := projectpaths.ChildResolved(tc, destination, rel)
				if !projectpaths.IsSpecialMutationRel(toChild.ScopeRel) {
					projectpaths.RecordResolvedMutation(ctx, tc, toChild)
					created, err := mkdirAgentPath(ctx, tc, resolvedMutationTarget(toChild), 0o755)
					result.Effects += created
					return err
				}
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
		filePair := FilePair{From: filepath.Join(pair.From, rel), To: to}
		if fastResolve && info.Mode().IsRegular() {
			fromChild := projectpaths.ChildResolved(tc, source, rel)
			toChild := projectpaths.ChildResolved(tc, destination, rel)
			if !projectpaths.IsSpecialMutationRel(fromChild.ScopeRel) && !projectpaths.IsSpecialMutationRel(toChild.ScopeRel) {
				projectpaths.RecordResolvedMutation(ctx, tc, fromChild)
				projectpaths.RecordResolvedMutation(ctx, tc, toChild)
				return page.AddResolved(filePair, fromChild, toChild, remaining, !destExisted)
			}
		}
		return page.Add(filePair, remaining)
	}
	finishDir := func(rel string, _ os.FileInfo) error {
		if !in.Preserve {
			return nil
		}
		if err := page.Flush(); err != nil {
			return err
		}
		target, err := assertCopyWritePath(ctx, t.Boundary, tc, filepath.Join(pair.To, rel), tc.ProfileID())
		if err != nil {
			return err
		}
		return preserveMetadata(ctx, tc, filepath.Join(pair.From, rel), resolvedMutationTarget(target))
	}
	err = walkMutationTree(ctx, root, base, ".", copyEntry, finishDir, memory)
	err = errors.Join(err, page.Flush())
	return err
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
