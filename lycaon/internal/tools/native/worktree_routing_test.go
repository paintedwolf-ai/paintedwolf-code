package native

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordingBlueprintObserver struct {
	paths []string
}

func (r *recordingBlueprintObserver) AfterWrite(_ context.Context, _, relPath string) {
	r.paths = append(r.paths, relPath)
}

func runGitRoutingTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, string(out))
	}
}

func TestMutationRoutingSeparatesAttachedLinkedMovedAndExternalPaths(t *testing.T) {
	repochangetest.Acquire(t)
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())

	sandboxDir := gitrepo.CanonicalDir(t.TempDir())
	primary := filepath.Join(sandboxDir, "workspace")
	secondary := filepath.Join(sandboxDir, "secondary")
	prefixCollision := filepath.Join(sandboxDir, "workspace-other")
	unrelated := filepath.Join(sandboxDir, "unrelated")
	siblingWT := filepath.Join(sandboxDir, "sibling-wt")

	for _, dir := range []string{primary, secondary, prefixCollision, unrelated} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(filepath.Join(dir, "pkg"), 0o755))
	}
	testutil.FailErr(t, "seed primary", os.WriteFile(filepath.Join(primary, "pkg/file.go"), []byte("package pkg\n"), 0o644))
	runGitRoutingTest(t, primary, "init", "-q")
	runGitRoutingTest(t, primary, "add", ".")
	runGitRoutingTest(t, primary, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "seed")
	runGitRoutingTest(t, primary, "worktree", "add", "-q", "-b", "feat/sib", siblingWT)

	eventsByDir := map[string][]repochange.Event{}
	t.Cleanup(repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.Kind == repochange.WorktreeChanged {
			eventsByDir[gitrepo.CanonicalDir(ev.ProjectDir)] = append(eventsByDir[gitrepo.CanonicalDir(ev.ProjectDir)], ev)
		}
	}))

	blueprintObs := &recordingBlueprintObserver{}
	drainBlueprint := SetBlueprintWriteObserver(blueprintObs)
	t.Cleanup(func() { testutil.FailErr(t, "drain blueprint observer", drainBlueprint(context.Background())) })

	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "proj-routing", SessionID: "sess-routing"},
		Source: tools.InvocationSource{
			Roots: []projectroot.RootRef{
				{ID: "r1", Label: "app", Path: primary, IsPrimary: true},
				{ID: "r2", Label: "sec", Path: secondary},
			},
			ActiveRootID:        "r1",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject,
		},
	}

	// Prime sibling directory observation in the process catalog.
	cat := sourcecatalog.Process()
	wtRoot := sourcecatalog.Root{ID: "r1", Path: siblingWT}
	obsBefore, err := cat.Directories.ObserveDirectory(t.Context(), "proj-routing", wtRoot, "pkg", sourcecatalog.DirectoryRead{})
	testutil.FailErr(t, "prime sibling directory", err)

	primaryStart := repochange.CurrentEpoch(primary)
	secondaryStart := repochange.CurrentEpoch(secondary)

	// 1. Sibling edit, prefix collision, unrelated external edit, and unclassified empty change:
	// zero attached-root events or epoch advances; sibling catalog invalidated; hooks preserved.
	testutil.FailErr(t, "write new sibling file", os.WriteFile(filepath.Join(siblingWT, "pkg/added.go"), []byte("package pkg\n"), 0o644))
	afterSuccessfulMutationLocations(t.Context(), tctx,
		mutationLocation{abs: filepath.Join(siblingWT, "pkg/added.go"), display: "../sibling-wt/pkg/added.go"},
		mutationLocation{abs: filepath.Join(prefixCollision, ".env"), display: "../workspace-other/.env"},
		mutationLocation{abs: filepath.Join(unrelated, ".env"), display: "../unrelated/.env"},
		mutationLocation{display: "blueprint.md"},
	)
	repochange.UseDebounce(t.Context(), repochange.DefaultDebounce)()

	if len(eventsByDir[primary]) != 0 || len(eventsByDir[secondary]) != 0 {
		t.Fatalf("external/linked mutations emitted attached-root events: %v", eventsByDir)
	}
	if repochange.CurrentEpoch(primary) != primaryStart || repochange.CurrentEpoch(secondary) != secondaryStart {
		t.Fatal("external/linked mutations advanced attached-root epochs")
	}
	if len(blueprintObs.paths) != 1 || blueprintObs.paths[0] != "blueprint.md" {
		t.Fatalf("blueprint observer saw %v, want [blueprint.md]", blueprintObs.paths)
	}
	obsAfter, err := cat.Directories.ObserveDirectory(t.Context(), "proj-routing", wtRoot, "pkg", sourcecatalog.DirectoryRead{})
	testutil.FailErr(t, "re-observe sibling directory", err)
	if obsAfter.Entries != obsBefore.Entries+1 || obsAfter.Invalidation <= obsBefore.Invalidation {
		t.Fatalf("sibling catalog was not invalidated: before=%+v after=%+v", obsBefore, obsAfter)
	}

	// 2. Worktree move: relocated checkout invalidates at its new canonical identity; old path does not.
	relocatedWT := filepath.Join(sandboxDir, "relocated-wt")
	runGitRoutingTest(t, primary, "worktree", "move", siblingWT, relocatedWT)
	relocatedRoot := sourcecatalog.Root{ID: "r1", Path: relocatedWT}
	relocObsBefore, err := cat.Directories.ObserveDirectory(t.Context(), "proj-routing", relocatedRoot, "pkg", sourcecatalog.DirectoryRead{})
	testutil.FailErr(t, "prime relocated directory", err)
	testutil.FailErr(t, "write relocated file", os.WriteFile(filepath.Join(relocatedWT, "pkg/moved_edit.go"), []byte("package pkg\n"), 0o644))
	notifyWorktreeChangedLocations(t.Context(), tctx,
		mutationLocation{abs: filepath.Join(relocatedWT, "pkg/moved_edit.go"), display: "moved_edit.go"},
	)
	repochange.UseDebounce(t.Context(), repochange.DefaultDebounce)()
	relocObsAfter, err := cat.Directories.ObserveDirectory(t.Context(), "proj-routing", relocatedRoot, "pkg", sourcecatalog.DirectoryRead{})
	testutil.FailErr(t, "re-observe relocated directory", err)
	if relocObsAfter.Invalidation <= relocObsBefore.Invalidation || len(eventsByDir[primary]) != 0 {
		t.Fatalf("relocated worktree routing failed: before=%+v after=%+v events=%v", relocObsBefore, relocObsAfter, eventsByDir[primary])
	}

	// 3. Cross-root rename, legitimate in-root absolute path, escaping scopeRel guard, and unknown-path root resync.
	notifyWorktreeChangedLocations(t.Context(), tctx,
		mutationLocation{rootID: "r1", scopeRel: "pkg/from.go", abs: filepath.Join(primary, "pkg/from.go")},
		mutationLocation{abs: filepath.Join(secondary, "pkg/to.go")},
		mutationLocation{rootID: "r1", scopeRel: "../escape/.env", abs: filepath.Join(unrelated, ".env")},
	)
	repochange.UseDebounce(t.Context(), repochange.DefaultDebounce)()
	if len(eventsByDir[primary]) != 1 || !slices.Equal(eventsByDir[primary][0].Paths, []string{"pkg/from.go"}) {
		t.Fatalf("primary events = %+v, want [pkg/from.go]", eventsByDir[primary])
	}
	if len(eventsByDir[secondary]) != 1 || !slices.Equal(eventsByDir[secondary][0].Paths, []string{"pkg/to.go"}) {
		t.Fatalf("secondary events = %+v, want [pkg/to.go]", eventsByDir[secondary])
	}

	// Unknown paths inside secondary root publish a whole-root resync without touching primary.
	notifyWorktreeChangedLocations(t.Context(), tctx, mutationLocation{rootID: "r2"})
	repochange.UseDebounce(t.Context(), repochange.DefaultDebounce)()
	if len(eventsByDir[primary]) != 1 {
		t.Fatalf("secondary resync touched primary: %+v", eventsByDir[primary])
	}
	if len(eventsByDir[secondary]) != 2 || !repochange.StructuralPaths(eventsByDir[secondary][1]).Full {
		t.Fatalf("secondary resync did not emit full resync: %+v", eventsByDir[secondary])
	}
}
