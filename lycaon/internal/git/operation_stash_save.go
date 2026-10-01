package git

import (
	"context"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
)

// A stash is a working-tree commit with HEAD, index, and optional untracked
// parents. Store that recovery object before cleaning any selected live paths.
func (p *operationPlan) saveStash(ctx context.Context, dir string, opts gitexec.Opts, headIndex, index operationIndex) ([]byte, error) {
	head, err := resolveCommit(ctx, dir, "HEAD")
	if err != nil {
		return nil, err
	}
	indexTree, err := restoreGit(ctx, dir, opts, "write-tree")
	if err != nil {
		return nil, err
	}
	// Include HEAD paths deleted from the index so add --update can observe a
	// staged deletion, including a file recreated afterward in the working tree.
	working := maps.Clone(index)
	for key, entry := range headIndex {
		if _, ok := working[key]; !ok {
			working[key] = entry
		}
	}
	if err := publishIndexDelta(ctx, dir, opts, index, working); err != nil {
		return nil, err
	}
	tracked := []string{}
	for start := 0; start < len(p.request.Paths); start += 32 {
		paths := p.request.Paths[start:min(start+32, len(p.request.Paths))]
		selected, err := readOperationIndex(ctx, dir, opts, paths...)
		if err != nil {
			return nil, err
		}
		for key := range selected {
			tracked = append(tracked, key.path)
		}
	}
	tracked = uniquePaths(tracked)
	if err := stashPathCommand(ctx, dir, opts, []string{"add", "--update"}, tracked); err != nil {
		return nil, err
	}
	workingTree, err := restoreGit(ctx, dir, opts, "write-tree")
	if err != nil {
		return nil, err
	}
	untrackedTree, untracked, err := p.stashUntracked(ctx, dir, opts, working)
	if err != nil {
		return nil, err
	}
	headTree, err := optionalObject(ctx, dir, head+"^{tree}")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(indexTree)) == headTree && strings.TrimSpace(string(workingTree)) == headTree && len(untracked) == 0 {
		return nil, nil
	}
	message := p.request.Message
	if message == "" {
		message = "Selected changes"
	}
	indexCommit, err := stashCommit(ctx, dir, opts, indexTree, "Index: "+message, head)
	if err != nil {
		return nil, err
	}
	parents := []string{head, indexCommit}
	if len(untracked) > 0 {
		commit, err := stashCommit(ctx, dir, opts, untrackedTree, "Untracked files: "+message)
		if err != nil {
			return nil, err
		}
		parents = append(parents, commit)
	}
	commit, err := stashCommit(ctx, dir, opts, workingTree, message, parents...)
	if err != nil {
		return nil, err
	}
	if _, err := restoreGit(ctx, dir, opts, "stash", "store", "-m", message, commit); err != nil {
		return nil, err
	}
	// Restore against the original scoped index, so newly staged files are
	// removed too; the caller publishes only this index delta to the live index.
	if _, err := restoreGit(ctx, dir, opts, "read-tree", strings.TrimSpace(string(indexTree))); err != nil {
		return nil, err
	}
	if err := stashPathCommand(ctx, dir, opts, []string{"restore", "--source=" + head, "--staged", "--worktree"}, tracked); err != nil {
		return nil, err
	}
	return nil, stashPathCommand(ctx, dir, opts, []string{"clean", "--force"}, untracked)
}

func stashCommit(ctx context.Context, dir string, opts gitexec.Opts, tree []byte, message string, parents ...string) (string, error) {
	args := []string{"commit-tree", strings.TrimSpace(string(tree)), "-m", message}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	out, err := restoreGit(ctx, dir, opts, args...)
	return strings.TrimSpace(string(out)), err
}

func stashPathCommand(ctx context.Context, dir string, opts gitexec.Opts, command, paths []string) error {
	for start := 0; start < len(paths); start += 32 {
		args := append([]string{"--literal-pathspecs"}, command...)
		args = append(args, "--")
		args = append(args, paths[start:min(start+32, len(paths))]...)
		if _, err := restoreGit(ctx, dir, opts, args...); err != nil {
			return err
		}
	}
	return nil
}

func (p *operationPlan) stashUntracked(ctx context.Context, dir string, opts gitexec.Opts, tracked operationIndex) ([]byte, []string, error) {
	if !p.request.IncludeUntracked {
		return nil, nil, nil
	}
	paths := []string{}
	args := append([]string{"--literal-pathspecs", "ls-files", "--others", "--exclude-standard", "-z", "--"}, p.request.Paths...)
	err := gitexec.RunRecords(ctx, dir, args, p.opts, func(raw []byte) error {
		path, err := parseRepoPath(raw)
		if err != nil {
			return err
		}
		if _, ok := tracked[operationIndexKey{path, 0}]; !ok {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil || len(paths) == 0 {
		return nil, nil, err
	}
	opts.IndexFile += ".untracked"
	if _, err := restoreGit(ctx, dir, opts, "read-tree", "--empty"); err != nil {
		return nil, nil, err
	}
	if err := stashPathCommand(ctx, dir, opts, []string{"add"}, paths); err != nil {
		return nil, nil, err
	}
	tree, err := restoreGit(ctx, dir, opts, "write-tree")
	return tree, paths, err
}
