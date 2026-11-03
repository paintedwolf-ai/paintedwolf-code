package git

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/gitrepo"
)

// sequenceKinds are the history-rewriting operations whose interrupted state
// Git keeps in the git directory until --continue or --abort.
var sequenceKinds = map[string][]string{
	"cherry-pick": {"sequencer", "CHERRY_PICK_HEAD"},
	"revert":      {"sequencer", "REVERT_HEAD"},
	"rebase":      {"rebase-merge", "rebase-apply"},
}

// sequenceState is copied into the rehearsal for a continuation or abort.
var sequenceState = []string{"sequencer", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "ORIG_HEAD", "MERGE_MSG", "AUTO_MERGE", "REBASE_HEAD"}

// continuing reports a request that resumes or abandons an interrupted
// operation of its own kind.
func (r OperationRequest) continuing() bool {
	return r.Action == "continue" || r.Action == "abort" || r.Action == "skip"
}

// validRepositoryPaths admits repository-relative pathspecs; "." is the
// whole tree for the kinds that accept it.
func validRepositoryPaths(paths []string, whole bool) error {
	if len(paths) > DefaultGitCommitMaxPaths {
		return operationRefusal("explicit_path_limit")
	}
	for _, path := range paths {
		clean := filepath.Clean(path)
		if clean == "." && whole {
			continue
		}
		if path == "" || clean == "." || filepath.IsAbs(path) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return operationRefusal("explicit_repository_paths_required", path)
		}
	}
	return nil
}

// add stages explicit paths; Mode "all" adds removals and untracked files
// (add -A), "update" tracked changes only (add -u).
func (p *operationPlan) add() error {
	r := p.request
	if len(r.Paths) == 0 && r.Mode == "" {
		return operationRefusal("explicit_paths_required")
	}
	if err := validRepositoryPaths(r.Paths, true); err != nil {
		return err
	}
	p.args = []string{"add"}
	switch r.Mode {
	case "":
	case "all":
		p.args = append(p.args, "--all")
	case "update":
		p.args = append(p.args, "--update")
	default:
		return operationRefusal("invalid_add_mode")
	}
	p.args = append(append(p.args, "--"), r.Paths...)
	return nil
}

// reset moves HEAD (soft, mixed, hard) to a pinned commit, or with paths
// resets those index entries from it.
func (p *operationPlan) reset(ctx context.Context, dir string) error {
	r := p.request
	ref := r.Ref
	if ref == "" {
		ref = "HEAD"
	}
	oid, err := p.pin(ctx, dir, ref)
	if err != nil {
		return err
	}
	if len(r.Paths) > 0 {
		if r.Mode != "" && r.Mode != "mixed" {
			return operationRefusal("invalid_reset_mode")
		}
		if err := validRepositoryPaths(r.Paths, true); err != nil {
			return err
		}
		p.args = append([]string{"reset", "--quiet", oid, "--"}, r.Paths...)
		return nil
	}
	mode := r.Mode
	if mode == "" {
		mode = "mixed"
	}
	switch mode {
	case "soft", "mixed", "hard", "keep", "merge":
	default:
		return operationRefusal("invalid_reset_mode")
	}
	p.args = []string{"reset", "--quiet", "--" + mode, oid, "--"}
	return nil
}

// clean removes untracked files (Mode "dirs" also directories) under paths.
// Ignored files are outside the rehearsal and are never removed.
func (p *operationPlan) clean() error {
	r := p.request
	if err := validRepositoryPaths(r.Paths, true); err != nil {
		return err
	}
	p.args = []string{"clean", "-f"}
	switch r.Mode {
	case "":
	case "dirs":
		p.args = append(p.args, "-d")
	default:
		return operationRefusal("invalid_clean_mode")
	}
	p.args = append(append(p.args, "--"), r.Paths...)
	return nil
}

// checkoutPaths writes paths from the index, or from a pinned commit into
// both index and work tree (git checkout [REF] -- PATH).
func (p *operationPlan) checkoutPaths(ctx context.Context, dir string) error {
	r := p.request
	if len(r.Paths) == 0 {
		return operationRefusal("explicit_paths_required")
	}
	if err := validRepositoryPaths(r.Paths, true); err != nil {
		return err
	}
	p.args = []string{"checkout", "--no-recurse-submodules"}
	if r.Ref != "" {
		oid, err := p.pin(ctx, dir, r.Ref)
		if err != nil {
			return err
		}
		p.args = append(p.args, oid)
	}
	p.args = append(append(p.args, "--"), r.Paths...)
	return nil
}

// detach checks out a pinned commit with HEAD detached.
func (p *operationPlan) detach(ctx context.Context, dir string) error {
	oid, err := p.pin(ctx, dir, p.request.Ref)
	if err != nil {
		return err
	}
	p.args = []string{"checkout", "--no-recurse-submodules", "--detach", oid, "--"}
	return nil
}

// sequence starts, continues or abandons cherry-pick, revert or rebase. A
// start pins every named commit; a conflict leaves Git's interrupted state.
func (p *operationPlan) sequence(ctx context.Context, dir string) error {
	r := p.request
	kind := r.Kind
	if r.continuing() {
		p.args = []string{kind, "--" + r.Action}
		// Resuming commits with the recorded message; no editor runs.
		p.opts.ExtraConfig = append(p.opts.ExtraConfig, "core.editor=true", "sequence.editor=true")
		return nil
	}
	if r.Action != "" && r.Action != "start" {
		return operationRefusal("invalid_" + strings.ReplaceAll(kind, "-", "_") + "_action")
	}
	refs := r.Refs
	if len(refs) == 0 && r.Ref != "" {
		refs = []string{r.Ref}
	}
	if len(refs) == 0 {
		return operationRefusal("ref_required")
	}
	p.args = []string{kind}
	switch kind {
	case "cherry-pick":
		if r.NoCommit {
			p.args = append(p.args, "--no-commit")
		}
	case "revert":
		p.args = append(p.args, "--no-edit")
		if r.NoCommit {
			p.args = append(p.args, "--no-commit")
		}
	case "rebase":
		if len(refs) != 1 {
			return operationRefusal("single_upstream_required")
		}
		p.opts.ExtraConfig = append(p.opts.ExtraConfig, "rebase.autoStash=false", "rebase.updateRefs=false", "core.editor=true", "sequence.editor=true")
	}
	p.args = append(p.args, "--end-of-options")
	for _, ref := range refs {
		oid, err := p.pin(ctx, dir, ref)
		if err != nil {
			return err
		}
		p.args = append(p.args, oid)
	}
	return nil
}

// sequenceInProgress names the interrupted operation the git directory holds.
func sequenceInProgress(repo gitrepo.Repo) (string, error) {
	for _, kind := range []string{"rebase", "cherry-pick", "revert"} {
		for _, name := range sequenceKinds[kind] {
			if _, err := os.Lstat(filepath.Join(repo.GitDir, name)); err == nil {
				return kind, nil
			} else if !os.IsNotExist(err) {
				return "", err
			}
		}
	}
	return "", nil
}

// copySequenceState carries the interrupted operation into the rehearsal.
func (p *preparedOperation) copySequenceState(repo gitrepo.Repo, scratch string) error {
	for _, name := range sequenceState {
		source := filepath.Join(repo.GitDir, name)
		info, err := os.Lstat(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if err := p.copyFile(source, filepath.Join(scratch, ".git", name)); err != nil {
				return err
			}
			continue
		}
		err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			rel, err := filepath.Rel(repo.GitDir, path)
			if err != nil {
				return err
			}
			return p.copyFileFrom(repo.GitDir, rel, filepath.Join(scratch, ".git", rel))
		})
		if err != nil {
			return err
		}
	}
	return nil
}
