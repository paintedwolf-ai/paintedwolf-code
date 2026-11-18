//go:build darwin

package confine_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSandboxMarkFindsADetachedDaemonAndNothingElse(t *testing.T) {
	self := requireLiveBoundary(t)
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	mark, err := confine.EnableSandboxMark()
	testutil.FailErr(t, "enable the sandbox mark", err)
	t.Cleanup(func() { confine.RemoveSandboxMark(mark) })

	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	boundary := confine.Confinement{Roots: []string{dir}, Network: confine.NetworkDeny}
	if code, out := confinedRun(t, self, boundary, "/usr/bin/python3", "-c", testutil.DetachedDaemonScript(pidPath, false)); code != 0 {
		t.Fatalf("start the confined daemon: exit=%d %s", code, out)
	}
	raw, err := os.ReadFile(pidPath) // #nosec G304 -- the test's own temp file
	testutil.FailErr(t, "read the daemon pid", err)
	daemon, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	testutil.FailErr(t, "parse the daemon pid", err)
	t.Cleanup(func() { _ = syscall.Kill(daemon, syscall.SIGKILL) })

	bystander := exec.Command("/bin/sleep", "30")
	testutil.FailErr(t, "start an unconfined bystander", bystander.Start())
	t.Cleanup(func() { _ = bystander.Process.Kill(); _ = bystander.Wait() })

	var found []int
	for _, inc := range confine.MarkedProcesses(mark) {
		found = append(found, inc.PID)
	}
	if !slices.Contains(found, daemon) {
		t.Fatalf("the detached daemon %d is not found under the mark: %v", daemon, found)
	}
	if slices.Contains(found, bystander.Process.Pid) || slices.Contains(found, os.Getpid()) {
		t.Fatalf("an unconfined process is found under the mark: %v", found)
	}

	confine.RemoveSandboxMark(mark)
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatalf("the mark survived its removal: %v", err)
	}
	if !osprocess.Alive(daemon) {
		t.Fatal("removing the mark is not supposed to touch its processes")
	}
}
