package native

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// TestGitCommitEmitsHeadMoved confirms a successful commit fans a HeadMoved
// signal (scoped to its repo) so history caches can refresh reactively.
func TestGitCommitEmitsHeadMoved(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	gittest.Run(t, dir, "config", "user.email", "test@example.com")
	gittest.Run(t, dir, "config", "user.name", "Test")
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi\n"), 0o644))

	var mu sync.Mutex
	var got repochange.Event
	events := 0
	absDir, err := filepath.Abs(dir)
	testutil.FailErr(t, "abs dir", err)
	t.Cleanup(repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir != absDir { // isolate from other tests' commits
			return
		}
		mu.Lock()
		got = ev
		events++
		mu.Unlock()
	}))

	mutations := GitMutations{Git: git.NewManager(), Boundary: nativefixture.Boundary(t)}
	_, _, err = mutations.Commit(context.Background(), nativefixture.Context(dir), GitCommitRequest{Message: "init", Paths: []string{"a.txt"}})
	testutil.FailErr(t, "commit", err)
	testutil.FailErr(t, "write new regression", os.WriteFile(filepath.Join(dir, "new_test.txt"), []byte("test\n"), 0o644))
	hash, staging, err := mutations.Commit(context.Background(), nativefixture.Context(dir), GitCommitRequest{
		Message: "init with regression", Paths: []string{"new_test.txt"}, Amend: true,
	})
	testutil.FailErr(t, "amend", err)
	if hash == "" || len(staging.Paths) != 1 || staging.Paths[0] != "new_test.txt" {
		t.Fatalf("amend = %q %+v", hash, staging)
	}
	if count := gittest.Run(t, dir, "rev-list", "--count", "HEAD"); count != "1\n" {
		t.Fatalf("amend created an extra commit: count = %q", count)
	}

	mu.Lock()
	defer mu.Unlock()
	if events != 2 {
		t.Fatalf("commit and amend emitted %d events, want 2", events)
	}
	if got.Kind != repochange.HeadMoved {
		t.Fatalf("emitted kind = %v, want HeadMoved", got.Kind)
	}
	if got.ProjectDir != absDir {
		t.Fatalf("emitted ProjectDir = %q, want %q", got.ProjectDir, absDir)
	}
}
