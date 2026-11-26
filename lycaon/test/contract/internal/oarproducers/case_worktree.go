package oarproducers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/indexwatch"
)

// Worktree cases: a command leaves the Git index behind an agent policy file
// the sandbox kept unchanged. Each kind is the index change after the
// command started.
const (
	worktreeStale    = "stale"
	worktreeLeftover = "leftover"
	worktreeConflict = "conflict"
)

// worktreeChanges are the Git plumbing commands that change only the index:
// a new entry for the file, its entry removed, and unmerged stages.
var worktreeChanges = map[string][]string{
	worktreeStale:    {"read-tree", "ours"},
	worktreeLeftover: {"rm", "-q", "--cached", "AGENTS.md"},
	worktreeConflict: {"read-tree", "-m", "base", "ours", "theirs"},
}

// worktreeFixture holds a repository whose AGENTS.md differs on the base,
// ours and theirs commits, with base checked out.
type worktreeFixture struct {
	once sync.Once
	dir  string
	err  error
}

func gitRun(ctx context.Context, dir string, args ...string) error {
	out, code, err := gitexec.Run(ctx, dir, args, gitexec.Opts{
		Profile:     gitexec.ProfileHermetic,
		Identity:    &gitexec.Identity{Name: "Painted Wolf test", Email: "test@paintedwolf.dev"},
		ExtraConfig: []string{"maintenance.auto=false"},
	})
	if err != nil || code != 0 {
		return fmt.Errorf("git %v: exit %d: %v: %s", args, code, err, out)
	}
	return nil
}

func (w *worktreeFixture) base(ctx context.Context) (string, error) {
	w.once.Do(func() {
		w.dir, w.err = os.MkdirTemp("", "casegen-worktree-*")
		if w.err != nil {
			return
		}
		write := func(body string) error {
			return os.WriteFile(filepath.Join(w.dir, "AGENTS.md"), []byte(body), 0o644)
		}
		steps := []func() error{
			func() error { return gitRun(ctx, w.dir, "init", "-q") },
			func() error { return write("base\n") },
			func() error { return gitRun(ctx, w.dir, "add", "-A") },
			func() error { return gitRun(ctx, w.dir, "commit", "-q", "-m", "base") },
			func() error { return gitRun(ctx, w.dir, "tag", "base") },
			func() error { return gitRun(ctx, w.dir, "checkout", "-q", "-b", "ours") },
			func() error { return write("ours\n") },
			func() error { return gitRun(ctx, w.dir, "commit", "-q", "-am", "ours") },
			func() error { return gitRun(ctx, w.dir, "checkout", "-q", "-b", "theirs", "base") },
			func() error { return write("theirs\n") },
			func() error { return gitRun(ctx, w.dir, "commit", "-q", "-am", "theirs") },
			func() error { return gitRun(ctx, w.dir, "checkout", "-q", "--detach", "base") },
		}
		for _, step := range steps {
			if w.err = step(); w.err != nil {
				return
			}
		}
	})
	return w.dir, w.err
}

// snapshot copies the fixture, captures its index as a command start would,
// then applies kind's index change. The returned cleanup removes the copy.
func (w *worktreeFixture) snapshot(ctx context.Context, kind string) (indexwatch.Snapshot, func(), error) {
	base, err := w.base(ctx)
	if err != nil {
		return indexwatch.Snapshot{}, func() {}, err
	}
	dir, err := os.MkdirTemp("", "casegen-worktree-case-*")
	if err != nil {
		return indexwatch.Snapshot{}, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.CopyFS(dir, os.DirFS(base)); err != nil {
		cleanup()
		return indexwatch.Snapshot{}, func() {}, err
	}
	snap := indexwatch.Take(dir)
	if !snap.Active() {
		cleanup()
		return indexwatch.Snapshot{}, func() {}, fmt.Errorf("no index snapshot of the worktree fixture")
	}
	if err := gitRun(ctx, dir, worktreeChanges[kind]...); err != nil {
		snap.Release()
		cleanup()
		return indexwatch.Snapshot{}, func() {}, err
	}
	return snap, cleanup, nil
}
