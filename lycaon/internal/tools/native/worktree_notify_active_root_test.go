package native

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Catalog invalidation must follow the root a mutated path resolves against
// under the session's active root, not the primary root.
func TestNotifyWorktreeChangedResolvesAgainstTheActiveRoot(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) }) // t.Context() is already canceled during cleanup

	base := t.TempDir()
	primary, secondary := filepath.Join(base, "app"), filepath.Join(base, "den")
	roots := []projectroot.RootRef{
		{ID: "primary", Label: "app", Path: primary, IsPrimary: true},
		{ID: "active", Label: "den", Path: secondary},
	}
	events := make(chan repochange.Event, 8)
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.Kind != repochange.WorktreeChanged {
			return
		}
		events <- ev
	})
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess"},
		Source: tools.InvocationSource{Roots: roots, ActiveRootID: "active",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject},
	}

	notifyWorktreeChanged(context.Background(), tctx, "notes.txt", "@app/other.txt")
	seen := map[string][]string{}
	deadline := time.After(5 * time.Second)
	for len(seen) < 2 {
		select {
		case ev := <-events:
			seen[ev.ProjectDir] = ev.Paths
		case <-deadline:
			t.Fatalf("timed out waiting for invalidations, got %v", seen)
		}
	}
	if paths := seen[secondary]; len(paths) != 1 || paths[0] != "notes.txt" {
		t.Fatalf("active-root invalidation = %v", paths)
	}
	if paths := seen[primary]; len(paths) != 1 || paths[0] != "other.txt" {
		t.Fatalf("labeled-root invalidation = %v", paths)
	}
}
