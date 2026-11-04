package workspace_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/workspace"
)

func fixtureMountedManager(t *testing.T, unmount func(context.Context, string, string) error) *workspace.Manager {
	t.Helper()
	mounted := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: adapter.AdapterFUSELinux,
		MountProbeFn: func(context.Context, string, string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonNone, "", nil
		},
		UnmountViewFn: unmount,
	})
	selector := adapter.NewSelector("linux", mounted, nil, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
	selector.RequireConfinement, selector.Confinement = true, fixtureConfinement{}
	branches := t.TempDir()
	privateGit, err := workspace.OpenPrivateGit(branches)
	testutil.FailErr(t, "open private Git", err)
	t.Cleanup(func() { _ = privateGit.Store().Close() })
	return workspace.NewManager(branches, t.TempDir()).WithSelector(selector).WithPrivateGit(privateGit)
}

func singleRoot(t *testing.T) []projectroot.RootRef {
	t.Helper()
	source := t.TempDir()
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0o644))
	return []projectroot.RootRef{{ID: "primary", Path: source, IsPrimary: true}}
}

// Fixture evidence: settlement fails in the fixture adapter's unmount step, not a kernel unmount.
func TestFailedSettlementKeepsTheBranchAndItsMetadataInCustody(t *testing.T) {
	failing := true
	manager := fixtureMountedManager(t, func(context.Context, string, string) error {
		if failing {
			return errors.New("still mounted")
		}
		return nil
	})
	roots := singleRoot(t)
	binding, _, err := manager.CreateWorkerWorkspaceFromSources(t.Context(), roots, roots, "primary", "job-custody")
	testutil.FailErr(t, "create workspace", err)

	err = manager.DestroyWorkerWorkspace(binding)
	if !errors.Is(err, claim.ErrSettlementUnresolved) {
		t.Fatalf("destroy = %v, want unresolved settlement", err)
	}
	if _, statErr := os.Stat(binding.Root); statErr != nil {
		t.Fatalf("failed settlement removed the branch root: %v", statErr)
	}
	if _, statErr := os.Stat(enginepaths.MetaDirForBranchRoot(binding.Root)); statErr != nil {
		t.Fatalf("failed settlement removed branch metadata: %v", statErr)
	}
	if _, live := consumer.LookupBranchEngine(binding.Root); !live {
		t.Fatal("a view still in custody lost its engine")
	}
	if _, live := workspace.LookupBranchClaimedView(binding.Root); !live {
		t.Fatal("a view still in custody lost its claim record")
	}

	failing = false
	testutil.FailErr(t, "retry destroy", manager.DestroyWorkerWorkspace(binding))
	if _, statErr := os.Stat(binding.Root); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("settled destroy kept the branch root: %v", statErr)
	}
	if _, live := consumer.LookupBranchEngine(binding.Root); live {
		t.Fatal("settled destroy left the engine registered")
	}
}

func TestRebuildRefusesABranchHoldingALiveView(t *testing.T) {
	manager := fixtureMountedManager(t, nil)
	roots := singleRoot(t)
	binding, _, err := manager.CreateWorkerWorkspaceFromSources(t.Context(), roots, roots, "primary", "job-live")
	testutil.FailErr(t, "create workspace", err)
	t.Cleanup(func() { _ = manager.DestroyWorkerWorkspace(binding) })

	// The same source and job name the same branch; a label change alone defeats the layout match.
	relabeled := append([]projectroot.RootRef(nil), roots...)
	relabeled[0].Label = "renamed"
	_, _, err = manager.CreateWorkerWorkspaceFromSources(t.Context(), relabeled, relabeled, "primary", "job-live")
	if !errors.Is(err, workspace.ErrBranchInUse) {
		t.Fatalf("rebuild over a live view = %v, want ErrBranchInUse", err)
	}
	if _, live := workspace.LookupBranchClaimedView(binding.Root); !live {
		t.Fatal("rebuild disturbed the live view")
	}
}

func TestMaterializedRootsAreManagedGitViewsForThePrimaryWorker(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve base", err)
	roots := make([]projectroot.RootRef, 0, 2)
	for _, id := range []string{"p", "s"} {
		dir := filepath.Join(base, id)
		testutil.FailErr(t, "mkdir root", os.MkdirAll(dir, 0o755))
		testutil.FailErr(t, "write root", os.WriteFile(filepath.Join(dir, "file.txt"), []byte(id+"\n"), 0o644))
		gittest.InitCommit(t, dir, "initial "+id)
		roots = append(roots, projectroot.RootRef{ID: id, Label: id, Path: dir, IsPrimary: id == "p"})
	}
	branches := t.TempDir()
	privateGit, err := workspace.OpenPrivateGit(branches)
	testutil.FailErr(t, "open private Git", err)
	t.Cleanup(func() { _ = privateGit.Store().Close() })
	manager := workspace.NewManager(branches, t.TempDir()).WithPrivateGit(privateGit)
	binding, _, err := manager.CreateWorkerWorkspaceFromSources(t.Context(), roots, roots, "p", "job-roots")
	testutil.FailErr(t, "create workspace", err)
	t.Cleanup(func() { _ = manager.DestroyWorkerWorkspace(binding) })

	worktree, finish, managed, err := privategit.PrepareManagerViewOperation(t.Context(), privateGit, binding.Root, "job-roots")
	testutil.FailErr(t, "prepare from the branch", err)
	if !managed || filepath.Dir(worktree) != filepath.Clean(binding.Root) {
		t.Fatalf("branch does not address the primary view: managed=%v worktree=%s", managed, worktree)
	}
	testutil.FailErr(t, "finish", finish())
	if out := gittest.Run(t, worktree, "log", "--format=%s"); !strings.Contains(out, "initial p") {
		t.Fatalf("primary view history = %q", out)
	}
}
