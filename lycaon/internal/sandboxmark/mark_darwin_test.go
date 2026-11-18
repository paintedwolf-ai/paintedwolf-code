//go:build darwin

package sandboxmark

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The profile has the engine's shape for the mark: its folder denied as the
// control-plane tree is, the own marker admitted last.
func markedProfile(dir string) string {
	return "(version 1)(allow default)" +
		"(deny file-write* (subpath " + strconv.Quote(dir) + "))" +
		"(allow file-write-data (literal " + strconv.Quote(filepath.Join(dir, ownName)) + "))"
}

func TestProcessesFindADetachedDaemonUnderTheMarkAndNothingElse(t *testing.T) {
	testutil.SkipIfShort(t, "starts sandboxed daemons")
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	mark, err := Enable()
	testutil.FailErr(t, "enable the mark", err)
	t.Cleanup(func() { Remove(mark) })

	pidPath := filepath.Join(t.TempDir(), "daemon.pid")
	launch := func(profile, pidFile string) int {
		out, err := exec.Command("/usr/bin/sandbox-exec", "-p", profile, "/usr/bin/python3", "-c", testutil.DetachedDaemonScript(pidFile, false)).CombinedOutput()
		testutil.FailErr(t, "start a sandboxed daemon: "+string(out), err)
		raw, err := os.ReadFile(pidFile) // #nosec G304 -- the test's own temp file
		testutil.FailErr(t, "read the daemon pid", err)
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		testutil.FailErr(t, "parse the daemon pid", err)
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		return pid
	}
	daemon := launch(markedProfile(mark), pidPath)
	// Another engine's profile, and one that may write the whole folder.
	otherEngine := launch(markedProfile(filepath.Join(filepath.Dir(mark), "other")), filepath.Join(t.TempDir(), "other.pid"))
	broad := launch("(version 1)(allow default)", filepath.Join(t.TempDir(), "broad.pid"))
	bystander := exec.Command("/bin/sleep", "30")
	testutil.FailErr(t, "start an unconfined bystander", bystander.Start())
	t.Cleanup(func() { _ = bystander.Process.Kill(); _ = bystander.Wait() })

	var found []int
	for _, inc := range Processes(mark) {
		found = append(found, inc.PID)
	}
	if !slices.Contains(found, daemon) {
		t.Fatalf("the detached daemon %d is not found under the mark: %v", daemon, found)
	}
	for _, other := range []int{otherEngine, broad, bystander.Process.Pid, os.Getpid()} {
		if slices.Contains(found, other) {
			t.Fatalf("process %d outside the mark is found under it: %v", other, found)
		}
	}
}

func TestOwnPathNamesTheCurrentMarkOnly(t *testing.T) {
	current.mu.Lock()
	saved := current.dir
	current.dir = ""
	current.mu.Unlock()
	t.Cleanup(func() { current.mu.Lock(); current.dir = saved; current.mu.Unlock() })
	if own := OwnPath(); own != "" {
		t.Fatalf("OwnPath = %q with no mark enabled", own)
	}
	current.mu.Lock()
	current.dir = "/marks/7-x"
	current.mu.Unlock()
	if own := OwnPath(); own != filepath.Join("/marks/7-x", ownName) {
		t.Fatalf("OwnPath = %q", own)
	}
}
