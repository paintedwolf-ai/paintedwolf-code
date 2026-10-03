package git

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

// Conversion settings are read once and pinned on both rehearsal and execution.
// Paths, executable drivers, credentials, includes, and remote settings are not copied.
func (p *preparedOperation) prepareConfig(ctx context.Context, repo gitrepo.Repo, scratch string) error {
	for _, key := range []string{"core.autocrlf", "core.eol", "core.filemode", "core.ignorecase", "core.symlinks", "core.safecrlf", "merge.conflictStyle", "merge.renormalize", "merge.renames", "merge.renameLimit", "merge.directoryRenames", "merge.default"} {
		raw, code, err := gitexec.Run(ctx, repo.Root, []string{"config", "--get", key}, hermeticOpts(0))
		if err != nil {
			return err
		}
		if code == 1 {
			continue
		}
		if code != 0 {
			return fmt.Errorf("read Git conversion configuration failed")
		}
		p.opts.ExtraConfig = append(p.opts.ExtraConfig, key+"="+strings.TrimSuffix(string(raw), "\n"))
	}
	raw, code, err := gitexec.Run(ctx, repo.Root, []string{"config", "--bool", "--get", "core.sparseCheckout"}, hermeticOpts(0))
	if err != nil {
		return err
	}
	if code == 0 && strings.TrimSpace(string(raw)) == "true" {
		return operationRefusal("sparse_checkout_unsupported")
	}
	return p.copyFile(filepath.Join(repo.CommonDir, "info", "attributes"), filepath.Join(scratch, ".git", "info", "attributes"))
}

func (p *preparedOperation) copyIndex(ctx context.Context, repo gitrepo.Repo, scratch string) error {
	// The private index expands shared-index objects without changing the live index.
	tempIndex := filepath.Join(p.scratch, "source.index")
	if err := p.copyFile(filepath.Join(repo.GitDir, "index"), tempIndex); err != nil {
		return err
	}
	opts := p.opts
	opts.IndexFile = tempIndex
	if _, err := restoreGit(ctx, repo.Root, opts, "update-index", "--no-split-index"); err != nil {
		return err
	}
	return p.copyFile(tempIndex, filepath.Join(scratch, ".git", "index"))
}

func (p *preparedOperation) copyStashes(ctx context.Context, source, scratch string) error {
	if p.result.Before.Stash == "" {
		return nil
	}
	if _, err := restoreGit(ctx, scratch, p.opts, "update-ref", "refs/stash", p.result.Before.Stash); err != nil {
		return err
	}
	repo, ok := gitrepo.Discover(source)
	if !ok {
		return ErrNotRepository
	}
	return p.copyFile(filepath.Join(repo.CommonDir, "logs", "refs", "stash"), filepath.Join(scratch, ".git", "logs", "refs", "stash"))
}

// copyFile only writes inside the invocation-owned scratch directory.
func (p *preparedOperation) copyFile(source, dest string) error {
	return p.copyFileFrom(filepath.Dir(source), filepath.Base(source), dest)
}

func (p *preparedOperation) copyFileFrom(sourceRoot, sourcePath, dest string) error {
	rel, err := filepath.Rel(p.scratch, dest)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("scratch destination escapes invocation")
	}
	if err := assertOperationParents(p.scratch, rel); err != nil {
		return err
	}
	source, err := os.OpenRoot(sourceRoot)
	if os.IsNotExist(err) {
		if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	info, err := source.Lstat(sourcePath)
	if os.IsNotExist(err) {
		if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	p.copied += info.Size()
	if p.copied > MaxOperationCopyBytes {
		return operationRefusal("operation_copy_limit")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := source.Readlink(sourcePath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
			return err
		}
		return os.Symlink(target, dest)
	}
	if !info.Mode().IsRegular() {
		return operationRefusal("unsupported_file_kind", sourcePath)
	}
	if existing, err := os.Lstat(dest); err == nil && existing.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(dest); err != nil {
			return err
		}
	}
	input, err := fseffect.OpenRead(fseffect.Location{Root: sourceRoot, Rel: sourcePath})
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: p.scratch, Rel: rel}, Source: io.LimitReader(input, MaxOperationCopyBytes+1), Mode: info.Mode().Perm(), DirMode: 0o700})
	return err
}

// Rehearsal materializes complete trees. Bound their declared blob sizes before
// checkout, rather than discovering an oversized repository after filling disk.
func (p *preparedOperation) checkTreeBudget(ctx context.Context, dir string) error {
	refs := []string{p.result.Before.Head}
	for _, oid := range p.refs {
		if oid != "" {
			refs = append(refs, oid)
		}
	}
	autoStash, err := optionalObject(ctx, dir, "MERGE_AUTOSTASH")
	if err != nil {
		return err
	}
	for _, stash := range []string{p.stashOID, autoStash} {
		if stash == "" {
			continue
		}
		refs = append(refs, stash, stash+"^2")
		third, err := optionalObject(ctx, dir, stash+"^3")
		if err != nil {
			return err
		}
		if third != "" {
			refs = append(refs, third)
		}
	}
	var bytes int64
	entries := 0
	for _, ref := range uniquePaths(refs) {
		err := gitexec.RunRecords(ctx, dir, []string{"ls-tree", "-r", "-l", "-z", ref}, p.opts, func(raw []byte) error {
			meta, _, ok := strings.Cut(string(raw), "\t")
			fields := strings.Fields(meta)
			if !ok || len(fields) != 4 {
				return fmt.Errorf("malformed tree entry")
			}
			if fields[1] != "blob" {
				return nil
			}
			size, err := strconv.ParseInt(fields[3], 10, 64)
			if err != nil || size < 0 {
				return fmt.Errorf("invalid tree blob size")
			}
			bytes += size
			entries++
			if bytes > MaxOperationCopyBytes || entries > 100000 {
				return operationRefusal("rehearsal_tree_limit")
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Review disables smudging; LFS paths are refused.
func checkOperationFilters(ctx context.Context, dir string, paths []string) error {
	for start := 0; start < len(paths); start += 32 {
		end := min(start+32, len(paths))
		args := append([]string{"check-attr", "-z", "filter", "--"}, paths[start:end]...)
		raw, err := restoreGit(ctx, dir, hermeticOpts(0), args...)
		if err != nil {
			return err
		}
		fields := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
		if len(fields)%3 != 0 {
			return fmt.Errorf("malformed Git attribute records")
		}
		for i := 0; i < len(fields); i += 3 {
			if fields[i+1] != "filter" {
				return fmt.Errorf("unexpected Git attribute")
			}
			if fields[i+2] == "lfs" {
				return operationRefusal("lfs_change_unsupported", fields[i])
			}
		}
	}
	return nil
}

// A final symlink is reviewed as a link. A symlink ancestor would redirect
// preparatory reads or effects outside the named repository and is refused.
func assertOperationParents(root, relative string) error {
	clean := filepath.Clean(relative)
	if filepath.IsAbs(relative) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return operationRefusal("invalid_repository_path", relative)
	}
	for parent := filepath.Dir(clean); parent != "."; parent = filepath.Dir(parent) {
		info, err := os.Lstat(filepath.Join(root, parent))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return operationRefusal("operation_parent_not_directory", parent)
		}
	}
	return nil
}

func readOperationContent(root, relative string) (RestoreContent, error) {
	if err := assertOperationParents(root, relative); err != nil {
		return RestoreContent{}, err
	}
	scope, err := os.OpenRoot(root)
	if err != nil {
		return RestoreContent{}, err
	}
	defer func() { _ = scope.Close() }()
	info, err := scope.Lstat(relative)
	if os.IsNotExist(err) {
		return RestoreContent{}, nil
	}
	if err != nil {
		return RestoreContent{}, err
	}
	content := RestoreContent{Exists: true, Mode: gitFileMode(info.Mode())}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := scope.Readlink(relative)
		if err != nil {
			return RestoreContent{}, err
		}
		return readRestoreStream(content, strings.NewReader(link))
	}
	if !info.Mode().IsRegular() {
		return RestoreContent{}, operationRefusal("unsupported_file_kind", relative)
	}
	file, err := fseffect.OpenRead(fseffect.Location{Root: root, Rel: relative})
	if err != nil {
		return RestoreContent{}, err
	}
	defer func() { _ = file.Close() }()
	actual, err := file.Stat()
	if err != nil {
		return RestoreContent{}, err
	}
	if !os.SameFile(info, actual) {
		return RestoreContent{}, operationRefusal("repository_changed_during_review", relative)
	}
	return readRestoreStream(content, file)
}
