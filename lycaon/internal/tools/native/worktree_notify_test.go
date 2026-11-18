package native

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestWriteTool_EmitsWorktreeChanged(t *testing.T) {
	repochangetest.Acquire(t)

	dir := t.TempDir()
	abs, _ := filepath.Abs(dir)
	events := make(chan repochange.Event, 1)
	t.Cleanup(repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir != abs || ev.Source != repochange.SourceMutation {
			return
		}
		select {
		case events <- ev:
		default:
		}
	}))

	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "hello.txt",
		"content": "hi",
	}, nativefixture.Context(dir))
	testutil.FailErr(t, "write", err)

	var got repochange.Event
	select {
	case got = <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("write tool did not report WorktreeChanged")
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
