package native

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestWriteTool_EmitsWorktreeChanged(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) }) // t.Context() is already canceled during cleanup

	dir := t.TempDir()
	abs, _ := filepath.Abs(dir)
	var got repochange.Event
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir != abs {
			return
		}
		got = ev
	})

	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "hello.txt",
		"content": "hi",
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "write", err)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		repochange.ResetDebouncerForTest(t.Context())
		if got.Kind == repochange.WorktreeChanged {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Kind != repochange.WorktreeChanged {
		t.Fatal("expected WorktreeChanged from write tool")
	}
	if got.Source != repochange.SourceMutation {
		t.Fatalf("source=%q", got.Source)
	}
	if got.ProjectDir != abs {
		t.Fatalf("ProjectDir=%q want %q", got.ProjectDir, abs)
	}
	found := false
	for _, p := range got.Paths {
		if p == "hello.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("paths=%v missing hello.txt", got.Paths)
	}
}
