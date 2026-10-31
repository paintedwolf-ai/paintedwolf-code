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
	nestedHeads   map[string]string
	expectedIndex operationIndex
	expectedState RepositoryState
}

type operationVisitKey struct{}

func recordOperationVisits(ctx context.Context, count int) {
	if observe, ok := ctx.Value(operationVisitKey{}).(func(int)); ok {
		observe(count)
	}
}

func (p *preparedOperation) close() { _ = os.RemoveAll(p.scratch) }

func prepareOperation(ctx context.Context, repo gitrepo.Repo, req OperationRequest) (*preparedOperation, error) {
	plan, err := planOperation(ctx, repo.Root, req)
	if err != nil {
		return nil, err
	}
	p := &preparedOperation{operationPlan: plan, inputs: map[string]RestoreContent{}, nestedHeads: map[string]string{}}
	p.result = OperationResult{Available: true, Paths: []string{}}
	var rawStatus string
	p.result.Before, rawStatus, err = readRepositoryStateAndStatus(ctx, repo.Root)
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
	if len(p.addPaths()) == 0 && p.inputs[filepath.Join(repo.GitDir, "index")].SHA256 == p.result.Before.Index {
		p.status = rawStatus
	} else {
		raw, err := restoreGit(ctx, repo.Root, hermeticOpts(0), p.statusArgs()...)
		if err != nil {
			return nil, err
		}
		p.status = string(raw)
	}
	recordOperationVisits(ctx, strings.Count(p.status, "\x00"))
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
	replaceEntries, _ := os.ReadDir(filepath.Join(repo.CommonDir, "refs", "replace"))
	if _, packedErr := os.Lstat(filepath.Join(repo.CommonDir, "packed-refs")); len(replaceEntries) > 0 || packedErr == nil {
		replacements, err := restoreGit(ctx, repo.Root, hermeticOpts(0), "for-each-ref", "--count=1", "--format=%(refname)", "refs/replace/")
		if err != nil {
			return err
		}
		if len(replacements) != 0 {
			return operationRefusal("replacement_history_unsupported")
		}
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
	_, sequenceKind := sequenceKinds[r.Kind]
	if len(state.Conflicts) > 0 && !(r.Kind == "merge" && (r.Action == "continue" || r.Action == "abort")) && !(sequenceKind && r.continuing()) && !r.indexOnly() && !(r.Kind == "reset" && r.Mode == "hard") {
		return operationRefusal("unresolved_conflicts", state.Conflicts...)
	}
	inProgress, err := sequenceInProgress(repo)
	if err != nil {
		return err
	}
	_, sequence := sequenceKinds[r.Kind]
	switch {
	case sequence && r.continuing():
		if inProgress != r.Kind {
			return operationRefusal("no_" + strings.ReplaceAll(r.Kind, "-", "_") + "_in_progress")
		}
	case inProgress != "" && !r.indexOnly() && !(r.Kind == "reset" && r.Mode == "hard"):
		return operationRefusal("operation_in_progress", inProgress)
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
	if len(p.addPaths()) == 0 {
		if err := p.checkTreeBudget(ctx, repo.Root); err != nil {
			return err
		}
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
	if p.result.Before.Head != "" && len(p.addPaths()) == 0 {
		if _, err := restoreGit(ctx, scratch, p.opts, "checkout", "--detach", p.result.Before.Head, "--"); err != nil {
			return err
		}
	}
	if p.result.Before.Branch != "" {
		ref := "refs/heads/" + p.result.Before.Branch
		if p.result.Before.Head != "" {
			if err := writeScratchRef(scratch, ref, p.result.Before.Head); err != nil {
				if _, err := restoreGit(ctx, scratch, p.opts, "update-ref", ref, p.result.Before.Head); err != nil {
					return err
				}
			}
		}
		if err := writeScratchRef(scratch, "HEAD", "ref: "+ref); err != nil {
			if _, err := restoreGit(ctx, scratch, p.opts, "symbolic-ref", "HEAD", ref); err != nil {
				return err
			}
		}
	}
	for ref, oid := range p.refs {
		if strings.HasPrefix(ref, "refs/heads/") && oid != "" {
			if err := writeScratchRef(scratch, ref, oid); err != nil {
				if _, err := restoreGit(ctx, scratch, p.opts, "update-ref", ref, oid); err != nil {
					return err
				}
			}
		}
	}
	if err := p.copyIndex(ctx, repo, scratch); err != nil {
		return err
	}
	if _, sequence := sequenceKinds[p.request.Kind]; sequence && p.request.continuing() {
		if err := p.copySequenceState(repo, scratch); err != nil {
			return err
		}
	}
	// Preserve merge control state only for the named continuation, abort, or staging.
	if (p.request.Kind == "merge" && (p.request.Action == "continue" || p.request.Action == "abort")) || p.request.indexOnly() {
		for _, name := range []string{"MERGE_HEAD", "MERGE_MSG", "MERGE_MODE", "ORIG_HEAD", "MERGE_AUTOSTASH", "AUTO_MERGE"} {
			if err := p.copyFile(filepath.Join(repo.GitDir, name), filepath.Join(scratch, ".git", name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *preparedOperation) copyLocalChanges(ctx context.Context, repo gitrepo.Repo, scratch string) ([]string, error) {
	if p.request.Kind == "stage" || (p.status == "" && len(p.addPaths()) == 0) {
		return nil, nil
	}
	var staged []string
	stagedArgs := []string{"diff", "--name-only", "--no-renames", "-z", "HEAD", "--"}
	if p.result.Before.Head == "" {
		stagedArgs = []string{"ls-files", "--cached", "-z", "--"}
	}
	stagedArgs = append(stagedArgs, p.addPaths()...)
	staged, err := gitPaths(ctx, repo.Root, stagedArgs...)
	if err != nil {
		return nil, err
	}
	recordOperationVisits(ctx, len(staged))
	indexPath := filepath.Join(repo.GitDir, "index")
	if err := p.capture(indexPath); err != nil {
		return nil, err
	}
	if p.inputs[indexPath].SHA256 != p.result.Before.Index {
		p.result.Before.Index = p.inputs[indexPath].SHA256
		if err := p.copyIndex(ctx, repo, scratch); err != nil {
			return nil, err
		}
	}
	dirtyArgs := []string{"ls-files", "--modified", "--deleted", "--others", "--exclude-standard", "-z"}
	if paths := p.addPaths(); len(paths) > 0 {
		dirtyArgs = append(dirtyArgs, "--")
		dirtyArgs = append(dirtyArgs, paths...)
	}
	dirty, err := gitPaths(ctx, repo.Root, dirtyArgs...)
	if err != nil {
		return nil, err
	}
	recordOperationVisits(ctx, len(dirty))
	if paths := p.addPaths(); len(paths) > 0 {
		trackedArgs := append([]string{"ls-files", "--cached", "-z", "--"}, paths...)
		tracked, err := gitPaths(ctx, repo.Root, trackedArgs...)
		if err != nil {
			return nil, err
		}
		recordOperationVisits(ctx, len(tracked))
		dirty = append(dirty, tracked...)
	}
	dirty = uniquePaths(append(dirty, staged...))
	for _, path := range dirty {
		if err := assertOperationParents(repo.Root, path); err != nil {
			return nil, err
		}
	}
	for _, path := range dirty {
		if p.request.Kind == "add" {
			if info, err := os.Lstat(filepath.Join(repo.Root, path)); err == nil && info.IsDir() {
				if err := p.copyNestedRepo(ctx, repo.Root, scratch, path); err != nil {
					return nil, err
				}
				continue
			}
		}
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
	var before RepositoryState
	var err error
	if !p.result.Before.Dirty && len(p.result.Before.Conflicts) == 0 && len(p.addPaths()) == 0 && len(dirty) == 0 {
		before, err = readRepositoryMetadata(ctx, scratch)
	} else {
		before, err = readRepositoryState(ctx, scratch)
	}
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
	if !p.request.indexOnly() {
		// Conflicting operations are reviewed too: conflict markers are file effects.
		candidates := append([]string{}, dirty...)
		if p.result.Before.Head != "" && after.Head != "" && p.result.Before.Head != after.Head {
			changed, err := gitPaths(ctx, scratch, "diff", "--name-only", "--no-renames", "-z", p.result.Before.Head, after.Head, "--")
			if err != nil {
				return err
			}
			candidates = append(candidates, changed...)
		}
		changedArgs := []string{"ls-files", "--modified", "--deleted", "--others", "--exclude-standard", "-z"}
		changed, err := gitPaths(ctx, scratch, changedArgs...)
		if err != nil {
			return err
		}
		candidates = append(candidates, changed...)
		if err := p.prepareFiles(repo.Root, scratch, uniquePaths(candidates)); err != nil {
			return err
		}
	}
	indexAfter, err := readOperationIndex(ctx, scratch, hermeticOpts(0))
	if err != nil {
		return err
	}
	if err := p.prepareIndexFiles(ctx, scratch, indexBefore, indexAfter); err != nil {
		return err
	}
	p.result.Paths = uniquePaths(p.result.Paths)
	if !hasOperationAttributes(repo, scratch, p.result.Paths, indexBefore, indexAfter) {
		return nil
	}
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
	for path, expected := range p.nestedHeads {
		actual, err := ResolveOptionalObject(ctx, filepath.Join(repo.Root, path), "HEAD^{commit}")
		if err != nil || actual != expected {
			return operationRefusal("repository_changed_during_review", path)
		}
	}

	state, err := readRepositoryMetadata(ctx, repo.Root)
	if err != nil {
		return err
	}
	if state.Head != p.result.Before.Head || state.Branch != p.result.Before.Branch || state.Stash != p.result.Before.Stash || state.MergeHead != p.result.Before.MergeHead {
		return operationRefusal("repository_changed_during_review")
	}
	raw, err := restoreGit(ctx, repo.Root, hermeticOpts(0), p.statusArgs()...)
	if err != nil {
		return err
	}
	if string(raw) != p.status {
		return operationRefusal("repository_changed_during_review")
	}
	recordOperationVisits(ctx, strings.Count(string(raw), "\x00"))
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
