//go:build darwin || linux

package platform_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/testutil"
)

// listAs lists dir as another user, root when user is empty, and reports whether it could.
func listAs(t *testing.T, user, dir string) bool {
	t.Helper()
	args := []string{"-n"}
	if user != "" {
		args = append(args, "-u", user)
	}
	return exec.Command("sudo", append(args, "/bin/ls", "-a", dir)...).Run() == nil
}

// A process running as another user reaches each view exactly as its adapter's fact says: this
// host's mount refuses root and every other user, and a materialized tree is ordinary files.
func TestOtherUsersReachAViewOnlyAsItsFactSays(t *testing.T) {
	if exec.Command("sudo", "-n", "true").Run() != nil {
		t.Skip("this host grants no non-interactive sudo, so no second user can try the view")
	}
	trunk, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve trunk", err)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(trunk, "file.txt"), []byte("x\n"), 0o644))
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve temp", err)

	selector := platform.NewSelector(platform.Config{Confinement: availableConfinement{}})
	mounted, _ := overBudgetChoice(t, selector, filepath.Join(tmp, "probe"), trunk)
	materialized := adapter.NewEagerCopyAdapter()
	for name, adp := range map[string]adapter.Adapter{"mounted": mounted, "materialized": materialized} {
		t.Run(name, func(t *testing.T) {
			view, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
				ViewID: "view-other-" + name, BackingDir: filepath.Join(tmp, name, "backing"), RuntimeDir: filepath.Join(tmp, name, "runtime"),
				QuiesceHook: func(context.Context, string) error { return nil }, FlushClientHook: func(context.Context, string) error { return nil },
				Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk, IsPrimary: true}},
			})
			testutil.FailErr(t, "Prepare", err)
			t.Cleanup(func() { _, _ = adp.Release(context.Background(), view.ViewID) })
			if _, err := os.Stat(filepath.Join(view.PrimaryWorkPath, "file.txt")); err != nil {
				t.Fatalf("the worker's own user cannot reach the view: %v", err)
			}
			switch access := adp.Facts().OtherUserAccess; access {
			case adapter.OtherUserAccessNone:
				if listAs(t, "", view.PrimaryWorkPath) || listAs(t, "nobody", view.PrimaryWorkPath) {
					t.Fatalf("another user reached a view whose fact is %q", access)
				}
			case adapter.OtherUserAccessFileModes:
				if !listAs(t, "", view.PrimaryWorkPath) {
					t.Fatalf("root could not reach a view whose fact is %q", access)
				}
			default:
				t.Fatalf("no other-user access fact: %q", access)
			}
		})
	}
}
