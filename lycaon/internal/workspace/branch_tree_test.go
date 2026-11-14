package workspace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

// A running worker's change list comes from its view, whichever provider prepared it.
func TestBranchTreeAnswersFromTheViewOnEveryProvider(t *testing.T) {
	cases := []struct {
		name    string
		manager func(t *testing.T, state string) *workspace.Manager
		kind    adapter.PreparationKind
	}{
		{"materialized", func(t *testing.T, state string) *workspace.Manager {
			return workspace.NewManager(enginepaths.WorkerBranchesRootUnder(state))
		}, adapter.PreparationMaterialized},
		{"mounted", func(t *testing.T, state string) *workspace.Manager {
			confine.TestingSetAutoConfine(t)
			if !confine.Available() {
				t.Skip("this host applies no confinement")
			}
			selector := workspace.EnforcedWorkspaceSelector()
			selector.BudgetFor = adapter.ZeroBudget
			branches := enginepaths.WorkerBranchesRootUnder(state)
			privateGit, err := workspace.OpenPrivateGit(branches)
			testutil.FailErr(t, "open private Git", err)
			return workspace.NewManager(branches).WithPrivateGit(privateGit).WithSelector(selector)
		}, adapter.PreparationMounted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, err := filepath.EvalSymlinks(t.TempDir())
			testutil.FailErr(t, "resolve state", err)
			t.Setenv("LYCAON_CONFIG_DIR", state)
			source := t.TempDir()
			for name, body := range map[string]string{"edit.txt": "before\n", "gone.txt": "gone\n", "kept.txt": "kept\n"} {
				testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(source, name), []byte(body), 0o644))
			}
			mgr := tc.manager(t, state)
			binding, err := mgr.CreateWorkerWorkspace(context.Background(), source, "job-tree")
			var refused *adapter.AdapterError
			if errors.As(err, &refused) {
				switch refused.ReasonCode {
				case adapter.ReasonFUSEDeviceUnavailable, adapter.ReasonFUSEHelperUnavailable, adapter.ReasonUnsupportedPlatform:
					t.Skipf("this host cannot mount a view: %s", refused.ReasonCode)
				default:
				}
			}
			testutil.FailErr(t, "create branch", err)
			t.Cleanup(func() { _ = mgr.DestroyWorkerWorkspace(context.Background(), binding) })
			if binding.View == nil || binding.View.Facts.PreparationKind != tc.kind {
				t.Fatalf("prepared %+v, want %s", binding.View, tc.kind)
			}

			testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(binding.Root, "edit.txt"), []byte("after\n"), 0o644))
			testutil.FailErr(t, "delete", os.Remove(filepath.Join(binding.Root, "gone.txt")))
			testutil.FailErr(t, "create", os.WriteFile(filepath.Join(binding.Root, "new.txt"), []byte("new\n"), 0o644))

			roots := []projectroot.RootRef{{ID: "primary", Label: "primary", Path: source, IsPrimary: true}}
			changes, ok, err := workspace.BranchTreeChanges(t.Context(), binding.Root, roots)
			testutil.FailErr(t, "changes", err)
			want := []workspacebaseline.Change{
				{Path: "edit.txt", Kind: workspacebaseline.ChangeModified},
				{Path: "gone.txt", Kind: workspacebaseline.ChangeDeleted},
				{Path: "new.txt", Kind: workspacebaseline.ChangeCreated},
			}
			if !ok || !slices.Equal(changes, want) {
				t.Fatalf("changes = %+v ok=%v, want %+v", changes, ok, want)
			}

			comparison, ok, err := workspace.BranchPathChange(t.Context(), binding.Root, roots, "edit.txt")
			testutil.FailErr(t, "path change", err)
			if !ok || comparison.Kind != workspacebaseline.ChangeModified || comparison.Before != "before\n" || comparison.After != "after\n" {
				t.Fatalf("comparison = %+v ok=%v", comparison, ok)
			}
			kept, _, err := workspace.BranchPathChange(t.Context(), binding.Root, roots, "kept.txt")
			testutil.FailErr(t, "unchanged path", err)
			if kept.Kind != "" || kept.Absent {
				t.Fatalf("an untouched file compared as %+v", kept)
			}

			testutil.FailErr(t, "move", os.Rename(filepath.Join(binding.Root, "kept.txt"), filepath.Join(binding.Root, "moved.txt")))
			move := workspacebaseline.PathMove{From: "kept.txt", To: "moved.txt"}
			unchanged, _, err := workspace.BranchMovesUnchanged(t.Context(), binding.Root, roots, []workspacebaseline.PathMove{move})
			testutil.FailErr(t, "moves", err)
			if !unchanged[move] {
				t.Fatal("a moved file with its base bytes was not recognised")
			}
		})
	}
}
