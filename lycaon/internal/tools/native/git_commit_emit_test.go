package native

import (
	"context"
	"encoding/json"
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

// TestGitCommitToolEmitsHeadMoved confirms a successful commit fans a HeadMoved
// signal (scoped to its repo) so history caches can refresh reactively.
func TestGitCommitToolEmitsHeadMoved(t *testing.T) {
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
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir != absDir { // isolate from other tests' commits
			return
		}
		mu.Lock()
		got = ev
		events++
		mu.Unlock()
	})

	tool := &GitCommitTool{Git: git.NewManager(), Boundary: nativefixture.Boundary(t)}
	_, err = tool.Run(context.Background(),
		map[string]any{"message": "init", "paths": []any{"a.txt"}}, nativefixture.Context(dir))
	testutil.FailErr(t, "commit tool run", err)
	testutil.FailErr(t, "write new regression", os.WriteFile(filepath.Join(dir, "new_test.txt"), []byte("test\n"), 0o644))
	out, err := tool.Run(context.Background(), map[string]any{
		"message": "init with regression", "paths": []any{"new_test.txt"}, "amend": true,
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "amend tool run", err)
	var receipt struct {
		Available bool     `json:"available"`
		Hash      string   `json:"hash"`
		Paths     []string `json:"paths"`
	}
	testutil.FailErr(t, "decode amend receipt", json.Unmarshal([]byte(out), &receipt))
	if !receipt.Available || receipt.Hash == "" || len(receipt.Paths) != 1 || receipt.Paths[0] != "new_test.txt" {
		t.Fatalf("amend receipt = %s", out)
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
