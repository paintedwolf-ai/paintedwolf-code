package native

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// Catalog invalidation must follow the root a mutated path resolves against
// under the session's active root, not the primary root.
func TestWorktreeChangesFollowTheActiveRoot(t *testing.T) {
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
		if ev.Kind != repochange.WorktreeChanged || (ev.ProjectDir != primary && ev.ProjectDir != secondary) {
			return
		}
		events <- ev
	})
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess"},
		Source: tools.InvocationSource{Roots: roots, ActiveRootID: "active",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject},
	}

	var locations []mutationLocation
	for _, path := range []string{"notes.txt", "@app/other.txt"} {
		resolved, err := projectpaths.ResolveRead(t.Context(), nil, tctx, path)
		testutil.FailErr(t, "resolve "+path, err)
		locations = append(locations, resolvedMutationLocation(resolved))
	}
	notifyWorktreeChangedLocations(context.Background(), tctx, locations...)
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

func TestWorktreeChangesExcludeExternalWrites(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) })

	root := filepath.Join(t.TempDir(), "workspace")
	var events []repochange.Event
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.Kind == repochange.WorktreeChanged && ev.ProjectDir == root {
			events = append(events, ev)
		}
	})
	tctx := tools.ToolContext{Source: tools.InvocationSource{
		Roots:               []projectroot.RootRef{{ID: "r1", Path: root, IsPrimary: true}},
		SourceWorkspaceKind: api.SourceWorkspaceKindProject,
	}}
	external := resolvedMutationLocation(projectpaths.Resolved{
		Abs: filepath.Join(root+"-other", ".env"), DisplayPath: "approved/external", External: true,
	})
	start := repochange.CurrentEpoch(root)
	notifyWorktreeChangedLocations(t.Context(), tctx, external)
	repochange.ResetDebouncerForTest(t.Context())
	if len(events) != 0 || repochange.CurrentEpoch(root) != start {
		t.Fatalf("external write changed the workspace: %v", events)
	}

	inside := resolvedMutationLocation(projectpaths.Resolved{
		Abs: filepath.Join(root, ".env"), DisplayPath: "approved/inside",
	})
	notifyWorktreeChangedLocations(t.Context(), tctx, external, inside)
	repochange.ResetDebouncerForTest(t.Context())
	if len(events) != 1 || len(events[0].Paths) != 1 || events[0].Paths[0] != ".env" {
		t.Fatalf("events = %#v, want one relative .env change", events)
	}
	if epoch := repochange.CurrentEpoch(root); epoch.Value != start.Value+1 {
		t.Fatalf("workspace epoch advanced from %v to %v", start, epoch)
	}
}
