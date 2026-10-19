//go:build linux && nixworkflow

package confine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxNixDaemonCannotUseAnOrdinarySocketGrant(t *testing.T) {
	bin, env := nixFixture(t)
	self := requireLinuxSandbox(t)
	root := t.TempDir()
	alias := filepath.Join(root, "approved-alias.sock")
	testutil.FailErr(t, "create fixture-owned daemon alias", os.Symlink(nixDaemonSocket, alias))
	for _, path := range []string{nixDaemonSocket, alias} {
		t.Run(path, func(t *testing.T) {
			box := denyConfinement(root)
			box.SocketGrants = []confine.SocketGrant{{ApprovedPath: path, ResolvedPath: nixDaemonSocket}}
			bound, lease := boundConfinement(t, box)
			defer lease.Close(context.Background())
			status, out := linuxConfinedRun(t, self, *bound, env, bin, "--offline", "--store", "unix://"+path, "store", "info", "--json")
			if status.ExitStatus() == 0 {
				t.Fatalf("ordinary grant opened delegated daemon authority:\n%s", out)
			}
			settled := lease.SettledRefusals(context.Background())
			if refusalTargets(settled)["host-service-delegation "+nixDaemonSocket] != confine.RecoverHostExecution {
				t.Fatalf("daemon alias lacked per-action host authority recovery: %+v\n%s", settled, out)
			}
		})
	}
}
