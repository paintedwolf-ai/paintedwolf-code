//go:build linux

package launchguard

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchverify"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The protected launch suite installs a root-owned fixture generation and a
// setuid copy of the real bootstrap, built with the fixture's layout linked
// in, and launches it as the ordinary test user. Fixture installation and
// lock inspection run as root through sudo, which the Linux confinement leg
// provides; nothing else in the suite is privileged.

const (
	fixtureModeEnv = "PW_LAUNCH_FIXTURE_MODE"
	fixtureDirEnv  = "PW_LAUNCH_FIXTURE_DIR"
	fixtureBinsEnv = "PW_LAUNCH_FIXTURE_BINARIES"
	fixtureHash    = "PW_LAUNCH_FIXTURE_DIGEST"
)

// protectedFacts is the fixture desktop's report.
type protectedFacts struct {
	PID             int                         `json:"pid"`
	UIDs            [3]int                      `json:"uids"`
	GIDs            [3]int                      `json:"gids"`
	Groups          []int                       `json:"groups"`
	Namespace       launchverify.NamespaceFacts `json:"namespace"`
	Executable      string                      `json:"executable"`
	Arguments       []string                    `json:"arguments"`
	Environment     []string                    `json:"environment_keys"`
	BrokerPeerPID   int                         `json:"broker_peer_pid"`
	BrokerPeerUID   int                         `json:"broker_peer_uid"`
	BrokerType      int                         `json:"broker_type"`
	LifetimeRegular bool                        `json:"lifetime_regular"`
	OpenBeyond      []int                       `json:"open_beyond"`
	InstallWrite    string                      `json:"install_write"`
	Remount         string                      `json:"remount"`
	ReadError       string                      `json:"read_error,omitempty"`
	BrokerOutput    string                      `json:"broker_output,omitempty"`
	BrokerExit      int                         `json:"broker_exit"`
	BrokerError     string                      `json:"broker_error,omitempty"`
	AbandonedEnded  bool                        `json:"abandoned_ended"`
	DetachedPID     int                         `json:"detached_pid,omitempty"`
	ExecOutside     string                      `json:"exec_outside"`
	ExecGeneration  string                      `json:"exec_generation"`
}

// launchFixture is one installed fixture layout.
type launchFixture struct {
	dir       string
	bootstrap string
	digest    string
	install   string
}

// runningLaunch is a started bootstrap and the facts its desktop reported.
type runningLaunch struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	facts protectedFacts
}

func startLaunch(t *testing.T, f launchFixture, env []string, args ...string) runningLaunch {
	t.Helper()
	cmd := exec.Command(f.bootstrap, args...)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	testutil.FailErr(t, "desktop input", err)
	stdout, err := cmd.StdoutPipe()
	testutil.FailErr(t, "desktop report", err)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stopProbe := make(chan struct{})
	probeErr := make(chan error, 1)
	go probeEarlyBrokerAgent(stopProbe, probeErr)
	testutil.FailErr(t, "start bootstrap", cmd.Start())
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- line
	}()
	var line string
	select {
	case line = <-lines:
	case <-time.After(30 * time.Second):
		close(stopProbe)
		t.Fatalf("protected desktop never reported; bootstrap stderr: %s", stderr.String())
	}
	close(stopProbe)
	if err := <-probeErr; err != nil {
		t.Fatal(err)
	}
	var facts protectedFacts
	if err := json.Unmarshal([]byte(line), &facts); err != nil {
		t.Fatalf("decode desktop report %q: %v; bootstrap stderr: %s", line, err, stderr.String())
	}
	return runningLaunch{cmd: cmd, stdin: stdin, facts: facts}
}

func refusedLaunch(t *testing.T, cmd *exec.Cmd, want string) {
	t.Helper()
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), want) {
		t.Fatalf("launch was not refused with %q: %v\n%s", want, err, out)
	}
}

func TestProtectedLaunchSealsTheDesktopInARootOwnedNamespace(t *testing.T) {
	f := installLaunchFixture(t)
	home := t.TempDir()
	coverDir := t.TempDir()
	env := []string{
		"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8",
		"LD_PRELOAD=/tmp/planted.so", "GTK_MODULES=planted", "LYCAON_SANDBOX=off", "SSL_CERT_FILE=/tmp/planted.pem",
		"GOCOVERDIR=" + coverDir, "GODEBUG=inittrace=1", "GOTRACEBACK=crash",
	}
	launch := startLaunch(t, f, env, "--fixture-argument", "--broker-probe")
	facts := launch.facts
	if facts.DetachedPID > 0 {
		t.Cleanup(func() { _ = syscall.Kill(facts.DetachedPID, syscall.SIGKILL) })
	}
	uid, gid := os.Getuid(), os.Getgid()
	if facts.ReadError != "" {
		t.Fatalf("desktop could not read its own namespace facts: %s", facts.ReadError)
	}
	if facts.UIDs != [3]int{uid, uid, uid} || facts.GIDs != [3]int{gid, gid, gid} {
		t.Fatalf("desktop credentials %v/%v, want the caller %d/%d on every credential", facts.UIDs, facts.GIDs, uid, gid)
	}
	groups, err := unix.Getgroups()
	testutil.FailErr(t, "caller groups", err)
	if !slices.Equal(facts.Groups, groups) {
		t.Fatalf("desktop groups %v, want %v", facts.Groups, groups)
	}
	if facts.Executable != filepath.Join(f.install, "bin/painted-wolf-code") {
		t.Fatalf("desktop executable %q is not the logical generation path", facts.Executable)
	}
	if len(facts.Arguments) != 3 || facts.Arguments[1] != "--fixture-argument" {
		t.Fatalf("desktop arguments %q", facts.Arguments)
	}
	for _, key := range facts.Environment {
		if !desktopEnvironmentKeys[key] && !strings.HasPrefix(key, "LC_") {
			t.Fatalf("environment key %q crossed the bootstrap: %v", key, facts.Environment)
		}
	}
	if !slices.Contains(facts.Environment, "HOME") || !slices.Contains(facts.Environment, "PATH") {
		t.Fatalf("session environment did not cross: %v", facts.Environment)
	}
	// The vault identity service's own attestation accepts the desktop.
	initial, err := launchverify.ReadNamespace("/proc", 0)
	testutil.FailErr(t, "read the initial namespace", err)
	if err := launchverify.ProtectedNamespacePair(initial, facts.Namespace, facts.Namespace); err != nil {
		t.Fatalf("service attestation refused the protected desktop: %v (%+v)", err, facts.Namespace)
	}
	// The supervisor is the bootstrap, which created the broker pair while
	// privileged and now holds only the caller's identity.
	if facts.BrokerPeerPID != launch.cmd.Process.Pid || facts.BrokerPeerUID != 0 || facts.BrokerType != unix.SOCK_SEQPACKET || !facts.LifetimeRegular {
		t.Fatalf("broker or lifetime descriptor not inherited from the supervisor: %+v", facts)
	}
	if len(facts.OpenBeyond) != 0 {
		t.Fatalf("bootstrap descriptors reached the desktop: %v", facts.OpenBeyond)
	}
	assertCallerCredentials(t, launch.cmd.Process.Pid)
	agentPID := findBrokerAgentPID(t)
	assertCallerCredentials(t, agentPID)
	assertBrokerAgentSealed(t, agentPID)
	// The broker starts programs outside the protected namespace, with the
	// caller's ordinary authority, and ends one whose caller abandons it.
	own, err := os.Readlink("/proc/self/ns/user")
	testutil.FailErr(t, "own user namespace", err)
	if facts.BrokerError != "" || facts.BrokerExit != 0 || !strings.Contains(facts.BrokerOutput, own) || !strings.Contains(facts.BrokerOutput, "NoNewPrivs:\t0") {
		t.Fatalf("brokered program did not run in the caller's namespace without no_new_privs: %q exit %d error %q (want %s)", facts.BrokerOutput, facts.BrokerExit, facts.BrokerError, own)
	}
	if !facts.AbandonedEnded {
		t.Fatal("an abandoned brokered program kept running")
	}
	if facts.DetachedPID <= 0 {
		t.Fatal("detached brokered program did not report a background PID")
	}
	// Only the generation (and the policy's system executables, none here)
	// can execute inside the namespace; the broker ran sh outside it.
	if facts.ExecOutside != "EACCES" || facts.ExecGeneration != "started" {
		t.Fatalf("execution domain: outside %s, generation %s", facts.ExecOutside, facts.ExecGeneration)
	}
	if facts.InstallWrite != "EROFS" || facts.Remount != "EPERM" {
		t.Fatalf("generation mount is not sealed: write %s, remount %s", facts.InstallWrite, facts.Remount)
	}
	// The desktop stays dumpable and traceable by anyone, so only the
	// root-owned namespace stands between it and a same-user reader.
	mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", facts.PID))
	if err == nil {
		_ = mem.Close()
		t.Fatal("a same-user process outside the namespace opened the protected desktop's memory")
	}
	if !errors.Is(err, unix.EACCES) {
		t.Fatalf("opening protected memory: %v, want EACCES", err)
	}
	if err := sudoFixture(t, "lock-free", f.dir, fixtureHash+"="+f.digest); err == nil {
		t.Fatal("the generation lifetime lock was free while the desktop ran")
	}
	testutil.FailErr(t, "close desktop input", launch.stdin.Close())
	testutil.FailErr(t, "supervisor exit", launch.cmd.Wait())
	testutil.FailErr(t, "generation lock released after the tree ended", sudoFixture(t, "lock-free", f.dir, fixtureHash+"="+f.digest))
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", facts.DetachedPID)); err != nil {
		t.Fatalf("detached background process %d did not survive desktop exit: %v", facts.DetachedPID, err)
	}
	entries, err := os.ReadDir(coverDir)
	testutil.FailErr(t, "read coverdir", err)
	if len(entries) != 0 {
		t.Fatalf("privileged bootstrap wrote to caller GOCOVERDIR: %v", entries)
	}
}

// probeEarlyBrokerAgent races /proc from before bootstrap start to verify a
// same-UID caller can never open /proc/<agent>/fd, /proc/<agent>/mem, or
// ptrace-attach the broker agent from the first observable window.
func probeEarlyBrokerAgent(stop <-chan struct{}, result chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for {
		select {
		case <-stop:
			result <- nil
			return
		default:
		}
		for _, pid := range brokerAgentPIDs() {
			if err := tryAttackBrokerAgent(pid); err != nil {
				result <- err
				return
			}
		}
	}
}

func brokerAgentPIDs() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil || !strings.Contains(string(raw), BrokerAgentArgument) {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

func tryAttackBrokerAgent(pid int) error {
	for _, target := range []string{
		fmt.Sprintf("/proc/%d/mem", pid),
		fmt.Sprintf("/proc/%d/fd/4", pid),
	} {
		if file, err := os.Open(target); err == nil {
			_ = file.Close()
			return fmt.Errorf("same-UID process opened broker agent %s", target)
		}
	}
	if err := syscall.PtraceAttach(pid); err == nil {
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, 0, nil)
		_ = syscall.PtraceDetach(pid)
		return fmt.Errorf("same-UID process ptrace-attached broker agent %d", pid)
	}
	return nil
}

func findBrokerAgentPID(t *testing.T) int {
	t.Helper()
	pids := brokerAgentPIDs()
	if len(pids) == 0 {
		t.Fatal("broker agent process not found in /proc")
	}
	return pids[len(pids)-1]
}

func assertBrokerAgentSealed(t *testing.T, pid int) {
	t.Helper()
	for _, target := range []string{
		fmt.Sprintf("/proc/%d/mem", pid),
		fmt.Sprintf("/proc/%d/fd/4", pid),
	} {
		file, err := os.Open(target)
		if err == nil {
			_ = file.Close()
			t.Fatalf("same-UID process opened broker agent %s", target)
		}
		if !errors.Is(err, unix.EACCES) {
			t.Fatalf("open %s: %v, want EACCES", target, err)
		}
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := syscall.PtraceAttach(pid); err == nil {
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, 0, nil)
		_ = syscall.PtraceDetach(pid)
		t.Fatalf("same-UID process ptrace-attached broker agent %d", pid)
	} else if !errors.Is(err, unix.EPERM) {
		t.Fatalf("ptrace broker agent %d: %v, want EPERM", pid, err)
	}
}

// assertCallerCredentials requires the running supervisor to hold only the
// caller's identity and no capability.
func assertCallerCredentials(t *testing.T, pid int) {
	t.Helper()
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	testutil.FailErr(t, "supervisor status", err)
	uid := fmt.Sprint(os.Getuid())
	for _, line := range strings.Split(string(status), "\n") {
		key, value, _ := strings.Cut(line, ":")
		fields := strings.Fields(value)
		switch key {
		case "Uid":
			if len(fields) != 4 || slices.ContainsFunc(fields, func(f string) bool { return f != uid }) {
				t.Fatalf("supervisor uids %v, want %s", fields, uid)
			}
		case "CapPrm", "CapEff", "CapAmb":
			if strings.Trim(value, " \t0") != "" {
				t.Fatalf("supervisor %s %s, want none", key, value)
			}
		}
	}
}

func TestProtectedLaunchRefusesWithoutTheSetuidTransition(t *testing.T) {
	f := installLaunchFixture(t)
	t.Run("ordinary copy", func(t *testing.T) {
		copied := filepath.Join(t.TempDir(), "pw-launch")
		raw, err := os.ReadFile(f.bootstrap)
		testutil.FailErr(t, "read bootstrap", err)
		testutil.FailErr(t, "copy bootstrap", os.WriteFile(copied, raw, 0o755))
		refusedLaunch(t, exec.Command(copied), "privileged bootstrap transition is unavailable")
	})
	t.Run("traced", func(t *testing.T) {
		cmd := exec.Command(f.bootstrap)
		cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true}
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		testutil.FailErr(t, "start traced bootstrap", cmd.Start())
		var status syscall.WaitStatus
		_, err := syscall.Wait4(cmd.Process.Pid, &status, 0, nil)
		testutil.FailErr(t, "traced exec stop", err)
		testutil.FailErr(t, "detach", syscall.PtraceDetach(cmd.Process.Pid))
		err = cmd.Wait()
		if err == nil || !strings.Contains(out.String(), "privileged bootstrap transition is unavailable") {
			t.Fatalf("a traced bootstrap was not refused: %v\n%s", err, out.String())
		}
	})
	t.Run("no new privileges", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNoNewPrivilegesExec$")
		cmd.Env = append(os.Environ(), "PW_LAUNCH_NNP_TARGET="+f.bootstrap)
		refusedLaunch(t, cmd, "privileged bootstrap transition is unavailable")
	})
	t.Run("stage by a caller", func(t *testing.T) {
		refusedLaunch(t, exec.Command(f.bootstrap, StageArgument), "administrator identity")
	})
}

// TestNoNewPrivilegesExec executes its target with no_new_privs set.
func TestNoNewPrivilegesExec(t *testing.T) {
	target := os.Getenv("PW_LAUNCH_NNP_TARGET")
	if target == "" {
		return
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		os.Exit(3)
	}
	if err := syscall.Exec(target, []string{target}, []string{}); err != nil {
		os.Exit(4)
	}
}

func TestProtectedLaunchRefusesAGenerationThePolicyDoesNotName(t *testing.T) {
	f := installLaunchFixture(t)
	testutil.FailErr(t, "tamper policy", sudoFixture(t, "tamper", f.dir, fixtureHash+"="+f.digest))
	refusedLaunch(t, exec.Command(f.bootstrap), "generation manifest differs from installed policy")
}
