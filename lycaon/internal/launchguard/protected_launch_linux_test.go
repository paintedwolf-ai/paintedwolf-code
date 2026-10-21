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
	ExecOutside     string                      `json:"exec_outside"`
	ExecGeneration  string                      `json:"exec_generation"`
	Exec            *engineProbe                `json:"exec,omitempty"`
	ModuleUser      string                      `json:"module_user"`
	ModuleLibrary   string                      `json:"module_library"`
	BrokerUserExec  string                      `json:"broker_user_exec,omitempty"`
}

// engineProbe mirrors the fixture's report on the engine's launch paths.
type engineProbe struct {
	HostOutput      string `json:"host_output"`
	HostCode        int    `json:"host_code"`
	HostError       string `json:"host_error,omitempty"`
	HostExecOutput  string `json:"host_execution_output"`
	HostExecCode    int    `json:"host_execution_code"`
	HostExecError   string `json:"host_execution_error,omitempty"`
	ConfinedOutput  string `json:"confined_output"`
	ConfinedError   string `json:"confined_error,omitempty"`
	ConfinedCode    int    `json:"confined_code"`
	ConfinedStderr  string `json:"confined_stderr,omitempty"`
	TerminalSurvive bool   `json:"terminal_survived_interrupt"`
	TerminalError   string `json:"terminal_error,omitempty"`
	RelayLossEnded  bool   `json:"relay_loss_ended_session"`
	RelayLossError  string `json:"relay_loss_error,omitempty"`
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
		t.Fatalf("protected desktop never reported; bootstrap stderr: %s", stderr.String())
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
	env := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LD_PRELOAD=/tmp/planted.so", "GTK_MODULES=planted", "LYCAON_SANDBOX=off", "SSL_CERT_FILE=/tmp/planted.pem"}
	launch := startLaunch(t, f, env, "--fixture-argument", "--broker-probe")
	facts := launch.facts
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
	// Only the generation (and the policy's system executables, none here)
	// can execute inside the namespace; the broker ran sh outside it.
	if facts.ExecOutside != "EACCES" || facts.ExecGeneration != "started" {
		t.Fatalf("execution domain: outside %s, generation %s", facts.ExecOutside, facts.ExecGeneration)
	}
	// Native code maps executable only from the administrator's closure; the
	// caller's own files stay executable for programs the broker starts.
	if facts.ModuleUser != "EPERM" || facts.ModuleLibrary != "ok" {
		t.Fatalf("module view: caller file %s, library root %s", facts.ModuleUser, facts.ModuleLibrary)
	}
	if facts.BrokerUserExec != "brokered-user-program" {
		t.Fatalf("brokered program from the caller's files: %q", facts.BrokerUserExec)
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
	waitLaunchEnd(t, launch.cmd)
	testutil.FailErr(t, "generation lock released after the tree ended", sudoFixture(t, "lock-free", f.dir, fixtureHash+"="+f.digest))
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

// waitLaunchEnd requires the supervisor to exit once the launch's last
// process has ended, broker agent included.
func waitLaunchEnd(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		testutil.FailErr(t, "supervisor exit", err)
	case <-time.After(20 * time.Second):
		t.Fatal("the supervisor outlived the launch's processes")
	}
}

// Inside the protected launch, the engine starts every program through the
// broker: unconfined ones in the caller's initial namespaces without
// no_new_privs, confined ones in their own nested namespaces built outside
// the protected one.
func TestProtectedLaunchRoutesEngineLaunchesThroughTheBroker(t *testing.T) {
	f := installLaunchFixture(t)
	env := []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	launch := startLaunch(t, f, env, "--exec-probe")
	probe := launch.facts.Exec
	if probe == nil {
		t.Fatalf("the fixture engine reported no launches: %+v", launch.facts)
	}
	initial, err := os.Readlink("/proc/self/ns/user")
	testutil.FailErr(t, "initial user namespace", err)
	protected := fmt.Sprintf("user:[%d]", launch.facts.Namespace.Inode)
	for name, run := range map[string]struct {
		output, err string
		code        int
	}{
		"host-internal":  {probe.HostOutput, probe.HostError, probe.HostCode},
		"host execution": {probe.HostExecOutput, probe.HostExecError, probe.HostExecCode},
	} {
		if run.err != "" || run.code != 7 || !strings.Contains(run.output, initial) || !strings.Contains(run.output, "NoNewPrivs:\t0") {
			t.Fatalf("%s launch did not run outside the protected namespace with its exit status: %q code %d error %q (want %s)", name, run.output, run.code, run.err, initial)
		}
	}
	if probe.ConfinedError != "" && !strings.Contains(probe.ConfinedError, "exit status") || strings.Contains(probe.ConfinedOutput, initial) || strings.Contains(probe.ConfinedOutput, protected) || !strings.Contains(probe.ConfinedOutput, "NoNewPrivs:\t1") {
		t.Fatalf("confined launch did not run in its own nested namespace: %q code %d error %q stderr %q", probe.ConfinedOutput, probe.ConfinedCode, probe.ConfinedError, probe.ConfinedStderr)
	}
	if !probe.TerminalSurvive {
		t.Fatalf("a brokered terminal program did not survive Ctrl-C: %s", probe.TerminalError)
	}
	if !probe.RelayLossEnded {
		t.Fatalf("killing the relay did not end the brokered session: %s", probe.RelayLossError)
	}
	testutil.FailErr(t, "close desktop input", launch.stdin.Close())
	waitLaunchEnd(t, launch.cmd)
}
