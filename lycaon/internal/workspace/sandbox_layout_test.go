package workspace_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestCreateWorkerWorkspaceFromSourcesMultiRoot(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "a")
	secondary := filepath.Join(base, "b")
	for _, dir := range []string{primary, secondary} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))
		name := filepath.Base(dir) + ".txt"
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644))
	}
	roots := []projectroot.RootRef{
		{ID: "p", Label: "a", Path: primary, IsPrimary: true},
		{ID: "s", Label: "b", Path: secondary, IsPrimary: false},
	}
	mgr := workspace.NewManager(filepath.Join(base, "branches"))
	binding, layout, err := mgr.CreateWorkerWorkspaceFromSources(context.Background(), roots, roots, "p", "job-mr")
	testutil.FailErr(t, "CreateWorkerWorkspaceFromSources", err)
	primaryDir, err := projectroot.BranchDirForID(layout.Roots[0].ID)
	testutil.FailErr(t, "primary branch dir", err)
	secondaryDir, err := projectroot.BranchDirForID(layout.Roots[1].ID)
	testutil.FailErr(t, "secondary branch dir", err)
	if _, err := os.Stat(filepath.Join(binding.Root, primaryDir)); err != nil {
		t.Fatalf("primary branch directory missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, secondaryDir)); err != nil {
		t.Fatalf("secondary branch directory missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, primaryDir, "a.txt")); err != nil {
		t.Fatalf("primary snapshot missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, secondaryDir, "b.txt")); err != nil {
		t.Fatalf("secondary snapshot missing: %v", err)
	}
	meta, err := workspace.LoadJobMeta(enginepaths.MetaDirForBranchRoot(binding.Root))
	testutil.FailErr(t, "LoadJobMeta", err)
	if !meta.SnapshotComplete {
		t.Fatal("claimed branch must be a complete immutable snapshot")
	}
	testutil.FailErr(t, "remove branch file", os.Remove(filepath.Join(binding.Root, primaryDir, "a.txt")))
	if _, err := os.Stat(filepath.Join(binding.Root, primaryDir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("removed branch path reappeared")
	}
}

func TestConcurrentClaimsReuseOneCompleteBranch(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "project")
	testutil.FailErr(t, "mkdir project", os.MkdirAll(primary, 0o755))
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(primary, "main.go"), []byte("package main\n"), 0o644))
	roots := []projectroot.RootRef{{ID: "p", Label: "app", Path: primary, IsPrimary: true}}
	mgr := workspace.NewManager(filepath.Join(base, "branches"))

	type result struct {
		binding *workspace.Binding
		err     error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(1)
	for range 2 {
		go func() {
			start.Wait()
			binding, _, err := mgr.CreateWorkerWorkspaceFromSources(t.Context(), roots, roots, "p", "same-job")
			results <- result{binding: binding, err: err}
		}()
	}
	start.Done()
	first, second := <-results, <-results
	testutil.FailErr(t, "first concurrent claim", first.err)
	testutil.FailErr(t, "second concurrent claim", second.err)
	if first.binding.Root != second.binding.Root {
		t.Fatalf("branch roots differ: %q %q", first.binding.Root, second.binding.Root)
	}
	if body, err := os.ReadFile(filepath.Join(first.binding.Root, "main.go")); err != nil || string(body) != "package main\n" {
		t.Fatalf("shared branch body = %q err=%v", body, err)
	}
	meta, err := workspace.LoadJobMeta(enginepaths.MetaDirForBranchRoot(first.binding.Root))
	testutil.FailErr(t, "load shared branch metadata", err)
	if !meta.SnapshotComplete {
		t.Fatal("shared branch is incomplete")
	}
}

func TestCreateWorkerWorkspaceFromSourcesSingleRootFlat(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "project")
	testutil.FailErr(t, "mkdir project", os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "only.txt"), []byte("x"), 0o644))
	roots := []projectroot.RootRef{{ID: "p", Label: "root", Path: dir, IsPrimary: true}}
	mgr := workspace.NewManager(filepath.Join(base, "branches"))
	binding, _, err := mgr.CreateWorkerWorkspaceFromSources(context.Background(), roots, roots, "p", "job-1")
	testutil.FailErr(t, "CreateWorkerWorkspaceFromSources", err)
	if _, err := os.Stat(filepath.Join(binding.Root, "only.txt")); err != nil {
		t.Fatalf("flat snapshot missing file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, "root")); !os.IsNotExist(err) {
		t.Fatal("single-root sandbox must be flat")
	}
}

func TestCreateWorkerWorkspaceFromSourcesMountedClaimZeroLowerWalksAndFormatV2Meta(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	secondary := filepath.Join(base, "secondary")
	for _, dir := range []string{primary, secondary} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "pkg"), 0o755))
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "pkg", "code.go"), []byte("package pkg\n"), 0o644))
	}
	roots := []projectroot.RootRef{
		{ID: "p", Label: "primary", Path: primary, IsPrimary: true},
		{ID: "s", Label: "secondary", Path: secondary, IsPrimary: false},
	}

	mountedAdp := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: adapter.AdapterNFSDarwin,
		TargetOS:  runtime.GOOS,
		MountProbeFn: func(_ context.Context, _, _ string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonNone, "", nil
		},
	})
	sel := adapter.NewSelector(runtime.GOOS, mountedAdp, mountedAdp, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
	sel.BudgetFor = adapter.ZeroBudget
	sel.Confinement = fixtureConfinement{}
	mgr := workspace.NewManager(filepath.Join(base, "branches")).WithSelector(sel)

	binding, _, err := mgr.CreateWorkerWorkspaceSelected(
		t.Context(),
		roots,
		roots,
		adapter.WorkspaceSelection{RootIDs: []string{"p", "s"}},
		[]string{"pkg/code.go"},
		"p",
		"job-mounted-v2",
	)
	testutil.FailErr(t, "CreateWorkerWorkspaceSelected mounted", err)
	defer func() { _ = mgr.DestroyWorkerWorkspace(binding) }()

	if binding.View == nil || binding.View.Core == nil {
		t.Fatalf("expected mounted view with Core, got %+v", binding.View)
	}
	if stats := binding.View.Core.Stats(); stats.LowerDirReads != 0 {
		t.Fatalf("expected 0 lower ReadDir walks during mounted workspace claim, got %d", stats.LowerDirReads)
	}

	meta, err := workspace.LoadJobMeta(enginepaths.MetaDirForBranchRoot(binding.Root))
	testutil.FailErr(t, "LoadJobMeta mounted v2", err)
	if meta.FormatVersion != workspace.JobMetaFormatV2 ||
		meta.PreparationKind != adapter.PreparationMounted ||
		meta.VisibilityMode != adapter.VisibilityLive ||
		!meta.PendingGitInit ||
		meta.JournalPath == "" {
		t.Fatalf("expected Format-v2 mounted live JobMeta with journal and PendingGitInit, got %+v", meta)
	}

	snap, receipt, ok := workspace.BranchWorkspaceState(binding.Root)
	if !ok || snap.Readiness != claim.StateReady || snap.GitInit != claim.GitInitLazyPending || snap.Adapter.SourceVisibility != adapter.VisibilityLive || receipt.AdapterID != adapter.AdapterNFSDarwin {
		t.Fatalf("unexpected BranchWorkspaceState: ok=%v snap=%+v receipt=%+v", ok, snap, receipt)
	}
}

// fixtureConfinement stands in for enforcement around the fixture MountedAdapter. It proves nothing
// about denial; these tests exercise claim sequencing, not confinement.
type fixtureConfinement struct{}

func (fixtureConfinement) Available(context.Context, adapter.ProbeConfig) error    { return nil }
func (fixtureConfinement) VerifyView(context.Context, *adapter.PreparedView) error { return nil }
