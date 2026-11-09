package tools

import (
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

// When the session folder is not the primary root, recorded source locations
// must name the root the path actually resolved against, not the primary.
func TestRecordSourcePathResolvesAgainstTheActiveRoot(t *testing.T) {
	c := ToolContext{
		Identity: InvocationIdentity{ProjectID: "p"},
		Source:   InvocationSource{Roots: []projectroot.RootRef{
			{ID: "primary", Label: "app", Path: t.TempDir(), IsPrimary: true},
			{ID: "active", Label: "worktree", Path: t.TempDir()},
		}, ActiveRootID: "active"},
		Effects: InvocationEffects{Out: &ToolInvocationOut{}},
	}

	c.RecordSourcePath("notes.txt", api.NavigationEntryKindFile)
	c.RecordSourcePath("@app/other.txt", api.NavigationEntryKindFile)
	got := c.Effects.Out.SourceContext
	if got == nil || len(got.Locations) != 2 {
		t.Fatalf("locations = %+v", got)
	}
	if loc := got.Locations[0]; loc.RootID != "active" || loc.Path != "notes.txt" {
		t.Fatalf("bare path bound to %+v, want the active root", loc)
	}
	if loc := got.Locations[1]; loc.RootID != "primary" || loc.Path != "other.txt" {
		t.Fatalf("labeled path bound to %+v, want the labeled root", loc)
	}
}
