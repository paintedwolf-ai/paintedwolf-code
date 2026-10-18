//go:build linux && nixworkflow

package confine_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

const nixDaemonSocket = "/nix/var/nix/daemon-socket/socket"

func nixFixture(t *testing.T) (string, []string) {
	t.Helper()
	requireLinuxSandbox(t)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	bin, err := exec.LookPath("nix")
	testutil.FailErr(t, "required Nix client", err)
	bin, err = filepath.EvalSymlinks(bin)
	testutil.FailErr(t, "resolve Nix client", err)
	home := t.TempDir()
	env := []string{
		"PATH=" + filepath.Dir(bin) + ":/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + home,
		"XDG_CACHE_HOME=" + home, "NIX_USER_CONF_FILES=/dev/null",
		"NIX_CONFIG=experimental-features = nix-command flakes\naccept-flake-config = false\nsandbox = true\nsubstituters =\nbuilders =\n",
	}
	return bin, env
}

func nativeNix(t *testing.T, bin string, env []string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native Nix control %v: %v\n%s", args, err, out)
	}
	return out
}

func TestLinuxNixDaemonAuthorityIsActionScoped(t *testing.T) {
	bin, env := nixFixture(t)
	self := requireLinuxSandbox(t)
	args := []string{"--offline", "--store", "daemon", "store", "ping", "--json"}
	var response map[string]any
	testutil.FailErr(t, "native daemon ping response", json.Unmarshal(nativeNix(t, bin, env, args...), &response))
	if info, err := os.Stat(nixDaemonSocket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("required Nix daemon socket: %v, %v", info, err)
	}
	for _, hostExecution := range []bool{false, true, false} {
		box := denyConfinement(t.TempDir())
		box.HostExecution = hostExecution
		if hostExecution {
			status, out := linuxConfinedRun(t, self, box, env, bin, args...)
			if status.ExitStatus() != 0 {
				t.Fatalf("explicit action host execution could not ping daemon: %v\n%s", status, out)
			}
			continue
		}
		bound, lease := boundConfinement(t, box)
		status, out := linuxConfinedRun(t, self, *bound, env, bin, args...)
		if status.ExitStatus() == 0 {
			t.Fatalf("unapproved daemon connection succeeded:\n%s", out)
		}
		settled := lease.SettledRefusals(context.Background())
		if refusalTargets(settled)["network-outbound "+nixDaemonSocket] != confine.RecoverSocketPath {
			t.Fatalf("daemon refusal lacked the structured socket recovery: %+v\n%s", settled, out)
		}
		lease.Close(context.Background())
	}
}

func immutableNixState(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	db := filepath.Join(state, "db")
	testutil.FailErr(t, "private Nix database directory", os.Mkdir(db, 0o700))
	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "required SQLite backup runtime", err)
	// Back up through SQLite, not a file copy of a concurrently writable database.
	const backup = `import pathlib, sqlite3, sys
source = sqlite3.connect(pathlib.Path('/nix/var/nix/db/db.sqlite').as_uri() + '?mode=ro', uri=True)
target = sqlite3.connect(sys.argv[1])
source.backup(target)
target.close()
source.close()
`
	cmd := exec.CommandContext(t.Context(), python, "-I", "-c", backup, filepath.Join(db, "db.sqlite"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("private consistent Nix database snapshot: %v\n%s", err, out)
	}
	schema, err := os.ReadFile("/nix/var/nix/db/schema")
	testutil.FailErr(t, "read Nix schema version", err)
	testutil.FailErr(t, "write private schema version", os.WriteFile(filepath.Join(db, "schema"), schema, 0o400))
	testutil.FailErr(t, "seal private database", os.Chmod(filepath.Join(db, "db.sqlite"), 0o400))
	testutil.FailErr(t, "seal private database directory", os.Chmod(db, 0o500))
	testutil.FailErr(t, "seal private state directory", os.Chmod(state, 0o500))
	t.Cleanup(func() {
		_ = os.Chmod(state, 0o700)
		_ = os.Chmod(db, 0o700)
	})
	return state
}

func TestLinuxNixImmutableCachedShell(t *testing.T) {
	bin, env := nixFixture(t)
	self := requireLinuxSandbox(t)
	state := immutableNixState(t)
	storePath := filepath.Dir(filepath.Dir(bin))
	box := denyConfinement(t.TempDir())
	box.ReadRoots = []string{state, "/nix/store"}
	query := url.Values{"state": {state}, "read-only": {"true"}}
	store := "local?" + query.Encode()
	// The selected package is already pinned by the installation's profile.
	// No daemon sees this private database and no build or substitution is admitted.
	args := []string{"--offline", "--store", store, "shell", storePath, "--command", bin, "--version"}
	status, out := linuxConfinedRun(t, self, box, env, bin, args...)
	if status.ExitStatus() != 0 {
		t.Fatalf("immutable cached Nix shell: %v\n%s", status, out)
	}
}
