package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/navigationref"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// AC-WR08-01 / INV-WR-06: with active ≠ primary, every consumer keys the same
// path on the same root identity instead of re-resolving display strings.
func TestActiveRootIdentitySharedAcrossConsumers(t *testing.T) {
	fix := newMultiRootFixture(t)
	fix.activeID = "d"
	tctx := fix.tctx("s1")
	tctx.Effects = tools.InvocationEffects{Out: &tools.ToolInvocationOut{}}
	ctx := context.Background()

	// SourceContext: bare paths resolve against the active root, @label paths
	// against the labeled root.
	tctx.RecordSourcePath("secondary-sentinel.txt", api.NavigationEntryKindFile)
	tctx.RecordSourcePath("@lycaon/primary-sentinel.txt", api.NavigationEntryKindFile)
	locations := map[string]api.NavigationTarget{}
	for _, target := range tctx.Effects.Out.SourceContext.Locations {
		locations[target.RootID+"/"+target.Path] = target
	}
	if _, ok := locations["d/secondary-sentinel.txt"]; !ok {
		t.Fatalf("bare path lost the active-root identity: %+v", tctx.Effects.Out.SourceContext.Locations)
	}
	if _, ok := locations["p/primary-sentinel.txt"]; !ok {
		t.Fatalf("labeled path lost its root identity: %+v", tctx.Effects.Out.SourceContext.Locations)
	}

	// Rewind keys: mutation resolution records the active-root identity.
	var recorded []tools.PrimaryMutation
	tctx.Source.MutationRecorder = activeRootRecorder(func(_ context.Context, _, rootID, relPath string) {
		recorded = append(recorded, tools.PrimaryMutation{RootID: rootID, RelPath: relPath})
	})
	resolved, err := projectpaths.ResolveWrite(ctx, nil, tctx, "secondary-sentinel.txt")
	if err != nil {
		t.Fatalf("resolve write: %v", err)
	}
	if resolved.Root.ID != "d" || resolved.ScopeRel != "secondary-sentinel.txt" {
		t.Fatalf("write resolved = %+v", resolved)
	}
	if len(recorded) == 0 {
		t.Fatal("write recorded no primary mutation")
	}
	for _, mutation := range recorded {
		if mutation.RootID != "d" || mutation.RelPath != "secondary-sentinel.txt" {
			t.Fatalf("rewind key = %+v, want the active-root identity", mutation)
		}
	}

	// Baseline grammar is root-keyed, not active-keyed: the primary root is
	// bare, every other root is "@label/rel".
	if root, rel, ok := projectroot.ParseBaselinePath(fix.roots, "primary-sentinel.txt"); !ok || root.ID != "p" || rel != "primary-sentinel.txt" {
		t.Fatalf("bare baseline spelling = %q %q %v", root.ID, rel, ok)
	}
	if root, rel, ok := projectroot.ParseBaselinePath(fix.roots, "@lycaon-den/secondary-sentinel.txt"); !ok || root.ID != "d" || rel != "secondary-sentinel.txt" {
		t.Fatalf("labeled baseline spelling = %q %q %v", root.ID, rel, ok)
	}

	// Display grammar keys on the session folder: bare renders in the active
	// root, other roots render "@label/rel".
	if got := projectroot.DisplayPath(fix.roots, "d", "d", "secondary-sentinel.txt"); got != "secondary-sentinel.txt" {
		t.Fatalf("active-root display = %q", got)
	}
	if got := projectroot.DisplayPath(fix.roots, "d", "p", "primary-sentinel.txt"); got != "@lycaon/primary-sentinel.txt" {
		t.Fatalf("other-root display = %q", got)
	}

	// Navigation references: bare anchored paths bind to the session folder
	// root and @label matching is case-insensitive.
	p := &project.Project{ID: "proj-1", Roots: []project.Root{
		{ID: "p", Label: "lycaon", Path: fix.primaryDir, IsPrimary: true},
		{ID: "d", Label: "lycaon-den", Path: fix.secondaryDir},
	}}
	refs := navigationref.BuildProjectPathReferences(p,
		"See [den](secondary-sentinel.txt) and [app](@LYCAON/primary-sentinel.txt).", nil, "d")
	byRoot := map[string]string{}
	for _, ref := range refs {
		byRoot[ref.RootID] = ref.Path
	}
	if byRoot["d"] != "secondary-sentinel.txt" || byRoot["p"] != "primary-sentinel.txt" {
		t.Fatalf("navigation refs = %+v", refs)
	}
}

type activeRootRecorder func(context.Context, string, string, string)

func (f activeRootRecorder) RecordPrimaryMutation(ctx context.Context, sessionID, rootID, relPath string) {
	f(ctx, sessionID, rootID, relPath)
}
