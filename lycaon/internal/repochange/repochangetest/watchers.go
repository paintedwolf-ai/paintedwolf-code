// Package repochangetest provides scoped fixtures over real repository-change resources.
package repochangetest

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func Acquire(t testing.TB) {
	t.Helper()
	release := repochange.AcquireWatcherLifetime()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := release(ctx); err != nil {
			t.Errorf("release repository-change fixture: %v", err)
		}
	})
}

// Suite acquires streams before TestMain runs and returns its final cleanup.
func Suite() func() {
	release := repochange.AcquireWatcherLifetime()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := release(ctx); err != nil {
			panic("release repository-change suite: " + err.Error())
		}
	}
}

func Watch(t testing.TB, root string) {
	t.Helper()
	Acquire(t)
	var directories []repochange.WatchDirectory
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return err
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		directories = append(directories, repochange.WatchDirectory{Path: path, EntryCount: len(entries)})
		return nil
	})
	if err != nil {
		t.Fatalf("enumerate watcher fixture: %v", err)
	}
	repochange.SeedWatch(t.Context(), root, directories)
	t.Cleanup(func() { repochange.CloseRoot(root) })
	testutil.WaitFor(t, "watcher fixture coverage is complete", func() bool { return repochange.Coverage(root).Complete })
	if coverage := repochange.Coverage(root); !coverage.Complete {
		t.Fatalf("watcher fixture is incomplete: %+v", coverage)
	}
	repochange.WaitQuiet(t.Context(), root, repochange.DefaultDebounce, 5*time.Second)
}
