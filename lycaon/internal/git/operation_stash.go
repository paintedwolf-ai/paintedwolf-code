package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
)

// A private index limits stash capture and restoration to the selected paths.
func (p *operationPlan) runStash(ctx context.Context, dir string) ([]byte, int, error) {
	paths, err := p.stashPaths(ctx, dir)
	if err != nil {
		return nil, -1, err
	}
	scratch, err := os.MkdirTemp("", "git-stash-index-*")
	if err != nil {
		return nil, -1, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	opts := p.opts
	opts.IndexFile = filepath.Join(scratch, "index")
	if _, err := restoreGit(ctx, dir, opts, "read-tree", "HEAD"); err != nil {
		return nil, -1, err
	}
	headIndex, err := readOperationIndex(ctx, dir, opts)
	if err != nil {
		return nil, -1, err
	}
	if err := p.stageStashPaths(ctx, dir, opts, paths); err != nil {
		return nil, -1, err
	}
	before, err := readOperationIndex(ctx, dir, opts)
	if err != nil {
		return nil, -1, err
	}
	var out []byte
	var code int
	var runErr error
	if p.request.Action == "save" {
		out, runErr = p.saveStash(ctx, dir, opts, headIndex, before)
		if runErr != nil {
			// Preparing snapshots changes only the temporary index. A failed
			// save never publishes those staging changes into the live index.
			return out, -1, runErr
		}
	} else {
		out, code, runErr = gitexec.Run(ctx, dir, p.args, opts)
	}
	after, err := readOperationIndex(ctx, dir, opts)
	if err != nil {
		return out, code, fmt.Errorf("observe stash index: %w", err)
	}
	if err := publishIndexDelta(ctx, dir, p.opts, before, after); err != nil {
		return out, code, fmt.Errorf("publish stash index: %w", err)
	}
	return out, code, runErr
}

func (p *operationPlan) stageStashPaths(ctx context.Context, dir string, opts gitexec.Opts, paths []string) error {
	// An empty scope means no staged entries, not ls-files over the whole index.
	for start := 0; start < len(paths); start += 32 {
		batch := paths[start:min(start+32, len(paths))]
		base, err := readOperationIndex(ctx, dir, opts, batch...)
		if err != nil {
			return err
		}
		staged, err := readOperationIndex(ctx, dir, p.opts, batch...)
		if err != nil {
			return err
		}
		if err := publishIndexDelta(ctx, dir, opts, base, staged); err != nil {
			return err
		}
	}
	return nil
}

func (p *operationPlan) stashPaths(ctx context.Context, dir string) ([]string, error) {
	if p.request.Action == "save" {
		return p.request.Paths, nil
	}
	paths := []string{}
	collect := func(raw []byte) error {
		path, err := parseRepoPath(raw)
		if err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	}
	for _, ref := range []string{p.stashOID, p.stashOID + "^2"} {
		args := []string{"diff", "--name-only", "--no-renames", "-z", p.stashOID + "^1", ref, "--"}
		if err := gitexec.RunRecords(ctx, dir, args, p.opts, collect); err != nil {
			return nil, err
		}
	}
	untracked, err := optionalObject(ctx, dir, p.stashOID+"^3")
	if err != nil {
		return nil, err
	}
	if untracked != "" {
		if err := gitexec.RunRecords(ctx, dir, []string{"ls-tree", "-r", "--name-only", "-z", untracked, "--"}, p.opts, collect); err != nil {
			return nil, err
		}
	}
	return uniquePaths(paths), nil
}

// update-index consumes all removals and replacements under one index lock.
// NUL records preserve tabs, newlines, and leading dashes in repository paths.
func publishIndexDelta(ctx context.Context, dir string, opts gitexec.Opts, before, after operationIndex) error {
	changed := []string{}
	for key, entry := range before {
		if after[key] != entry {
			changed = append(changed, key.path)
		}
	}
	for key, entry := range after {
		if before[key] != entry {
			changed = append(changed, key.path)
		}
	}
	changed = uniquePaths(changed)
	if len(changed) == 0 {
		return nil
	}
	head, err := resolveCommit(ctx, dir, "HEAD")
	if err != nil {
		return err
	}
	var input strings.Builder
	for _, path := range changed {
		fmt.Fprintf(&input, "0 %s\t%s%c", strings.Repeat("0", len(head)), path, 0)
		for stage := 0; stage <= 3; stage++ {
			entry, ok := after[operationIndexKey{path, stage}]
			if !ok {
				continue
			}
			mode := uint32(0o100000 | entry.mode.Perm())
			if entry.mode&os.ModeSymlink != 0 {
				mode = 0o120000
			} else if entry.mode.IsDir() {
				mode = 0o160000
			}
			fmt.Fprintf(&input, "%o %s %d\t%s%c", mode, entry.oid, stage, path, 0)
		}
	}
	opts.Input = []byte(input.String())
	_, err = restoreGit(ctx, dir, opts, "update-index", "-z", "--index-info")
	return err
}
