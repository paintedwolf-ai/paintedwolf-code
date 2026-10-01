package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

type preparedOperation struct {
	operationPlan
	scratch       string
	result        OperationResult
	files         []RestoreFile
	inputs        map[string]RestoreContent
	status        string
	copied        int64
	previewBytes  int64
	expectedIndex operationIndex
	expectedState RepositoryState
}

func (p *preparedOperation) close() { _ = os.RemoveAll(p.scratch) }

func (m *Manager) prepareOperation(ctx context.Context, repo gitrepo.Repo, req OperationRequest) (*preparedOperation, error) {
	plan, err := planOperation(ctx, repo.Root, req)
	if err != nil {
		return nil, err
	}
	p := &preparedOperation{operationPlan: plan, inputs: map[string]RestoreContent{}}
	p.result = OperationResult{Available: true, Paths: []string{}}
	p.result.Before, err = readRepositoryState(ctx, repo.Root)
	if err != nil {
		return nil, err
	}
	if p.result.Before.Branch != "" {
		p.opts.ExtraConfig = append(p.opts.ExtraConfig, "branch."+p.result.Before.Branch+".mergeOptions=")
	}
	if err := p.checkState(ctx, repo); err != nil {
		return nil, err
	}
	if err := p.captureMetadata(repo); err != nil {
		return nil, err
	}
	raw, err := restoreGit(ctx, repo.Root, hermeticOpts(0), "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	p.status = string(raw)
	p.scratch, err = os.MkdirTemp("", "git-operation-*")
	if err != nil {
		return nil, err
	}
	if err = p.rehearse(ctx, repo); err != nil {
		p.close()
		return nil, err
	}
	if err = p.validate(ctx, repo); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

func (p *preparedOperation) checkState(ctx context.Context, repo gitrepo.Repo) error {
	replacements, err := restoreGit(ctx, repo.Root, hermeticOpts(0), "for-each-ref", "--count=1", "--format=%(refname)", "refs/replace/")
	if err != nil {
		return err
	}
	if len(replacements) != 0 {
		return operationRefusal("replacement_history_unsupported")
	}
	if _, err := os.Lstat(filepath.Join(repo.CommonDir, "info", "grafts")); err == nil {
		return operationRefusal("replacement_history_unsupported")
	} else if !os.IsNotExist(err) {
		return err
	}

	state := p.result.Before
	r := p.request
	if r.Kind == "merge" {
		continuing := r.Action == "continue" || r.Action == "abort"
		if continuing && state.MergeHead == "" {
			return operationRefusal("no_merge_in_progress")
		}
		if !continuing && state.MergeHead != "" {
			return operationRefusal("merge_in_progress")
		}
	}
	if len(state.Conflicts) > 0 && !(r.Kind == "merge" && (r.Action == "continue" || r.Action == "abort")) {
		return operationRefusal("unresolved_conflicts", state.Conflicts...)
	}
	for _, name := range []string{"rebase-merge", "rebase-apply", "sequencer", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if _, err := os.Lstat(filepath.Join(repo.GitDir, name)); err == nil {
			return operationRefusal("operation_in_progress", name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (p *preparedOperation) captureMetadata(repo gitrepo.Repo) error {
	paths := append(repo.LocalConfigFiles(), filepath.Join(repo.CommonDir, "info", "attributes"), filepath.Join(repo.CommonDir, "logs", "refs", "stash"))
	for _, name := range []string{"HEAD", "index", "MERGE_HEAD", "MERGE_MSG", "MERGE_MODE", "ORIG_HEAD", "MERGE_AUTOSTASH", "AUTO_MERGE", "info/sparse-checkout"} {
		paths = append(paths, filepath.Join(repo.GitDir, name))
	}
	for _, path := range paths {
		if err := p.capture(path); err != nil {
			return err
		}
	}
	return nil
}

func (p *preparedOperation) captureWorkspace(root, path string) error {
	content, err := readOperationContent(root, path)
	if err != nil {
		return err
	}
	content.Bytes = nil
	p.inputs[filepath.Join(root, path)] = content
	return nil
}

func (p *preparedOperation) capture(path string) error {
	content, err := readRestoreContent(path)
	if err != nil {
		return err
	}
	content.Bytes = nil
	p.inputs[path] = content
	return nil
}

func (p *preparedOperation) rehearse(ctx context.Context, repo gitrepo.Repo) error {
	scratch := filepath.Join(p.scratch, "repository")
	if err := p.checkTreeBudget(ctx, repo.Root); err != nil {
		return err
	}
	if err := p.prepareScratch(ctx, repo, scratch); err != nil {
		return err
	}
	dirty, err := p.copyLocalChanges(ctx, repo, scratch)
	if err != nil {
		return err
	}
	if err := p.copyStashes(ctx, repo.Root, scratch); err != nil {
		return err
	}
	return p.rehearseResult(ctx, repo, scratch, dirty)
}

func (p *preparedOperation) prepareScratch(ctx context.Context, repo gitrepo.Repo, scratch string) error {
	if _, err := restoreGit(ctx, p.scratch, p.opts, "clone", "--shared", "--no-checkout", "--no-hardlinks", "--", repo.Root, scratch); err != nil {
		return err
	}
	if err := p.prepareConfig(ctx, repo, scratch); err != nil {
		return err
	}
	// Materialize the baseline before copying the live index and dirty files.
	if _, err := restoreGit(ctx, scratch, p.opts, "checkout", "--detach", p.result.Before.Head, "--"); err != nil {
		return err
	}
	if p.result.Before.Branch != "" {
		ref := "refs/heads/" + p.result.Before.Branch
		if _, err := restoreGit(ctx, scratch, p.opts, "update-ref", ref, p.result.Before.Head); err != nil {
			return err
		}
		if _, err := restoreGit(ctx, scratch, p.opts, "symbolic-ref", "HEAD", ref); err != nil {
			return err
		}
	}
	for ref, oid := range p.refs {
		if strings.HasPrefix(ref, "refs/heads/") && oid != "" {
			if _, err := restoreGit(ctx, scratch, p.opts, "update-ref", ref, oid); err != nil {
				return err
			}
		}
	}
	if err := p.copyIndex(ctx, repo, scratch); err != nil {
		return err
	}
	// Preserve merge control state only for the named continuation or abort.
	if p.request.Kind == "merge" && (p.request.Action == "continue" || p.request.Action == "abort") {
		for _, name := range []string{"MERGE_HEAD", "MERGE_MSG", "MERGE_MODE", "ORIG_HEAD", "MERGE_AUTOSTASH", "AUTO_MERGE"} {
			if err := p.copyFile(filepath.Join(repo.GitDir, name), filepath.Join(scratch, ".git", name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *preparedOperation) copyLocalChanges(ctx context.Context, repo gitrepo.Repo, scratch string) ([]string, error) {
	dirty, err := gitPaths(ctx, repo.Root, "ls-files", "--modified", "--deleted", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	staged, err := gitPaths(ctx, repo.Root, "diff", "--name-only", "--no-renames", "-z", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	dirty = uniquePaths(append(dirty, staged...))
	for _, path := range dirty {
		if err := assertOperationParents(repo.Root, path); err != nil {
			return nil, err
		}
	}
	for _, path := range dirty {
		if err := p.captureWorkspace(repo.Root, path); err != nil {
			return nil, err
		}
		if err := p.copyFileFrom(repo.Root, path, filepath.Join(scratch, path)); err != nil {
			return nil, err
		}
	}
	return dirty, nil
}

func (p *preparedOperation) rehearseResult(ctx context.Context, repo gitrepo.Repo, scratch string, dirty []string) error {
	before, err := readRepositoryState(ctx, scratch)
	if err != nil {
		return err
	}
	indexBefore, err := readOperationIndex(ctx, scratch, hermeticOpts(0))
	if err != nil {
		return err
	}
	out, code, runErr := p.run(ctx, scratch)
	after, err := readRepositoryState(ctx, scratch)
	if err != nil {
		return err
	}
	p.expectedState = after
	p.result.ExitCode = code
	p.result.Diagnostics = string(out)
	p.result.After = p.result.Before // A failed rehearsal has no live effect.
	p.result.AfterObserved = true
	p.result.Status = operationStatus(before, after, code, runErr)
	if runErr != nil {
		return runErr
	}
	if p.result.Status == "failed" {
		return nil
	}
	// Conflicting operations are reviewed too: conflict markers are file effects.
	candidates := append([]string{}, dirty...)
	changed, err := gitPaths(ctx, scratch, "diff", "--name-only", "--no-renames", "-z", p.result.Before.Head, after.Head, "--")
	if err != nil {
		return err
	}
	candidates = append(candidates, changed...)
	changed, err = gitPaths(ctx, scratch, "ls-files", "--modified", "--deleted", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	candidates = append(candidates, changed...)
	if err := p.prepareFiles(repo.Root, scratch, uniquePaths(candidates)); err != nil {
		return err
	}
	indexAfter, err := readOperationIndex(ctx, scratch, hermeticOpts(0))
	if err != nil {
		return err
	}
	if err := p.prepareIndexFiles(ctx, scratch, indexBefore, indexAfter); err != nil {
		return err
	}
	p.result.Paths = uniquePaths(p.result.Paths)
	if err := checkOperationFilters(ctx, repo.Root, p.result.Paths); err != nil {
		return err
	}
	return checkOperationFilters(ctx, scratch, p.result.Paths)
}

func (p *preparedOperation) prepareFiles(root, scratch string, paths []string) error {
	for _, path := range paths {
		before, err := readOperationContent(root, path)
		if err != nil {
			return err
		}
		after, err := readOperationContent(scratch, path)
		if err != nil {
			return err
		}
		if sameContent(before, after) {
			continue
		}
		// Record every ancestor's attributes used by conversion and review.
		for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
			attr := filepath.Join(root, parent, ".gitattributes")
			if _, ok := p.inputs[attr]; !ok {
				if err := assertOperationParents(root, filepath.Join(parent, ".gitattributes")); err != nil {
					return err
				}
				if err := p.captureWorkspace(root, filepath.Join(parent, ".gitattributes")); err != nil {
					return err
				}
			}
			if parent == "." {
				break
			}
		}
		if err := p.appendFile(RestoreFile{Path: path, Before: before, After: after}); err != nil {
			return err
		}
	}
	return nil
}

func (p *preparedOperation) appendFile(file RestoreFile) error {
	size := int64(len(file.Before.Bytes) + len(file.After.Bytes))
	if size > sourceledger.MaxRevisionContentBytes-p.previewBytes {
		file.Before.Bytes, file.After.Bytes = nil, nil
	} else {
		p.previewBytes += size
	}
	p.files = append(p.files, file)
	p.result.Paths = append(p.result.Paths, file.Path)
	return nil
}

func (p *preparedOperation) validate(ctx context.Context, repo gitrepo.Repo) error {
	if err := p.checkState(ctx, repo); err != nil {
		return err
	}
	for path, expected := range p.inputs {
		rel, err := filepath.Rel(repo.Root, path)
		if err != nil {
			return err
		}
		var actual RestoreContent
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			actual, err = readOperationContent(repo.Root, rel)
		} else {
			actual, err = readRestoreContent(path)
		}
		if err != nil {
			return err
		}
		if !sameContent(expected, actual) {
			return operationRefusal("repository_changed_during_review", path)
		}
	}

	state, err := readRepositoryState(ctx, repo.Root)
	if err != nil {
		return err
	}
	if state.Head != p.result.Before.Head || state.Branch != p.result.Before.Branch || state.Stash != p.result.Before.Stash || state.MergeHead != p.result.Before.MergeHead {
		return operationRefusal("repository_changed_during_review")
	}
	raw, err := restoreGit(ctx, repo.Root, hermeticOpts(0), "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	if string(raw) != p.status {
		return operationRefusal("repository_changed_during_review")
	}
	for _, file := range p.files {
		if file.IndexOnly {
			continue
		}
		current, err := readOperationContent(repo.Root, file.Path)
		if err != nil {
			return err
		}
		if !sameContent(current, file.Before) {
			return operationRefusal("repository_changed_during_review", file.Path)
		}
	}
	return nil
}

func (p *preparedOperation) verifyFiles(ctx context.Context, root string) error {
	for _, file := range p.files {
		if file.IndexOnly {
			continue
		}
		actual, err := readOperationContent(root, file.Path)
		if err != nil {
			return err
		}
		if !sameContent(actual, file.After) {
			return fmt.Errorf("result differs from reviewed Git content: %s", file.Path)
		}
	}
	return p.verifyIndex(ctx, root)
}

func sameContent(a, b RestoreContent) bool {
	return a.Exists == b.Exists && a.Mode == b.Mode && a.SHA256 == b.SHA256
}
func uniquePaths(paths []string) []string {
	seen := map[string]bool{}
	for _, path := range paths {
		seen[path] = true
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
