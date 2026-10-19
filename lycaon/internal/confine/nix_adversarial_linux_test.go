//go:build linux && nixworkflow

package confine_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxNixColdDaemonBuildCannotInheritSocketAuthority(t *testing.T) {
	bin, env := nixFixture(t)
	bash := nixStoreFixtureInput(t, "PW_NIX_FIXTURE_BASH", "/bin/bash")
	system := map[string]string{"amd64": "x86_64-linux", "arm64": "aarch64-linux"}[runtime.GOARCH]
	expression := fmt.Sprintf(`builtins.derivation { name = "paintedwolf-refused-cold-build"; system = %q; builder = %q; args = [ "-c" "printf refused > $out" ]; }`, system, bash)
	self := requireLinuxSandbox(t)
	for _, ordinaryGrant := range []bool{false, true} {
		t.Run(fmt.Sprintf("ordinary grant %t", ordinaryGrant), func(t *testing.T) {
			box := denyConfinement(t.TempDir())
			if ordinaryGrant {
				box.SocketGrants = []confine.SocketGrant{{ApprovedPath: nixDaemonSocket, ResolvedPath: nixDaemonSocket}}
			}
			bound, lease := boundConfinement(t, box)
			defer lease.Close(context.Background())
			status, output := linuxConfinedRun(t, self, *bound, env, bin, "--offline", "--store", "daemon", "build", "--no-link", "--impure", "--expr", expression)
			if status.ExitStatus() == 0 || refusalTargets(lease.SettledRefusals(t.Context()))["host-service-delegation "+nixDaemonSocket] != confine.RecoverHostExecution {
				t.Fatalf("cold build escaped its client authority: %v\n%s", status, output)
			}
		})
	}
}

func TestLinuxNixSelectedToolchainCannotWriteHostProfilesOrStore(t *testing.T) {
	fixture := prepareNixWorkflows(t)
	outside := filepath.Join(t.TempDir(), "profile")
	storeWrite := filepath.Join("/nix/store", "pw-refused-"+filepath.Base(t.TempDir()))
	t.Cleanup(func() { _ = os.Remove(storeWrite) })
	testutil.FailErr(t, "write fixture-owned host profile sentinel", os.WriteFile(outside, []byte("unchanged"), 0o600))
	cases := [][]string{
		{"shell", filepath.Dir(filepath.Dir(fixture.bash)), "--command", fixture.bash, "-c", `printf changed > "$1"`, "nix-write", outside},
		{"shell", filepath.Dir(filepath.Dir(fixture.bash)), "--command", fixture.bash, "-c", `printf changed > "$1"`, "nix-write", storeWrite},
		{"profile", "add", "--profile", outside, filepath.Dir(filepath.Dir(fixture.bash))},
	}
	for index, arguments := range cases {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			result, err := fixture.run(t, t.Context(), execpkg.LaunchAgentCommand, fixture.bin, append([]string{"--offline", "--store", fixture.store}, arguments...)...)
			if err == nil && result != nil && result.ExitCode == 0 {
				t.Fatalf("selected toolchain gained host write authority: %+v", result)
			}
			content, err := os.ReadFile(outside)
			testutil.FailErr(t, "read host profile sentinel", err)
			if !bytes.Equal(content, []byte("unchanged")) {
				t.Fatal("selected toolchain changed the ungranted host profile")
			}
		})
	}
}
