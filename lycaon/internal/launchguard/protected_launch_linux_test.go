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
	"sync/atomic"
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
	fixtureModeEnv     = "PW_LAUNCH_FIXTURE_MODE"
	fixtureDirEnv      = "PW_LAUNCH_FIXTURE_DIR"
	fixtureBinsEnv     = "PW_LAUNCH_FIXTURE_BINARIES"
	fixtureHash        = "PW_LAUNCH_FIXTURE_DIGEST"
	fixtureVaultBinEnv = "PW_LAUNCH_FIXTURE_VAULT_BIN"
	fixtureUIDEnv      = "PW_LAUNCH_FIXTURE_UID"
	fixtureGIDEnv      = "PW_LAUNCH_FIXTURE_GID"
)

// protectedFacts is the fixture desktop's report.
type protectedFacts struct {
	ProcessStarts   map[int]uint64              `json:"process_starts"`
	DetachedStart   uint64                      `json:"detached_start"`
	LineagePIDs     []int                       `json:"lineage_pids,omitempty"`
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
	ProtectedLaunch string                      `json:"protected_launch"`
	GenerationFrame json.RawMessage             `json:"generation_frame"`
	LifetimeRegular bool                        `json:"lifetime_regular"`
	OpenBeyond      []string                    `json:"open_beyond"`
	InstallWrite    string                      `json:"install_write"`
	Remount         string                      `json:"remount"`
	ReadError       string                      `json:"read_error,omitempty"`
	BrokerOutput    string                      `json:"broker_output,omitempty"`
	BrokerExit      int                         `json:"broker_exit"`
	BrokerError     string                      `json:"broker_error,omitempty"`
	SupervisorState string                      `json:"supervisor_state,omitempty"`
	BrokerAgents    int                         `json:"broker_agents"`
	AbandonedEnded  bool                        `json:"abandoned_ended"`
	DetachedPID     int                         `json:"detached_pid,omitempty"`
	ExecOutside     string                      `json:"exec_outside"`
	ExecGeneration  string                      `json:"exec_generation"`
	Exec            *engineProbe                `json:"exec,omitempty"`
	ModuleUser      string                      `json:"module_user"`
	ModuleLibrary   string                      `json:"module_library"`
	BrokerUserExec  string                      `json:"broker_user_exec,omitempty"`
}

// engineProbe mirrors the fixture's report on the engine's launch paths.
type engineProbe struct {
	HostOutput             string `json:"host_output"`
	HostCode               int    `json:"host_code"`
	HostError              string `json:"host_error,omitempty"`
	HostExecOutput         string `json:"host_execution_output"`
	HostExecCode           int    `json:"host_execution_code"`
	HostExecError          string `json:"host_execution_error,omitempty"`
	ConfinedOutput         string `json:"confined_output"`
	ConfinedError          string `json:"confined_error,omitempty"`
	ConfinedCode           int    `json:"confined_code"`
	ConfinedStderr         string `json:"confined_stderr,omitempty"`
	PreparedOutput         string `json:"prepared_output"`
	PreparedCode           int    `json:"prepared_code"`
	PreparedError          string `json:"prepared_error,omitempty"`
	PreparedConfinedOutput string `json:"prepared_confined_output"`
	PreparedConfinedCode   int    `json:"prepared_confined_code"`
	PreparedConfinedError  string `json:"prepared_confined_error,omitempty"`
	TerminalSurvive        bool   `json:"terminal_survived_interrupt"`
	TerminalError          string `json:"terminal_error,omitempty"`
	RelayLossEnded         bool   `json:"relay_loss_ended_session"`
	RelayLossError         string `json:"relay_loss_error,omitempty"`
	BrowserEngineLossEnded bool   `json:"browser_engine_loss_ended"`
	BrowserEngineLossError string `json:"browser_engine_loss_error,omitempty"`
}

// launchFixture is one installed fixture layout.
type launchFixture struct {
	dir       string
	bootstrap string
	digest    string
	install   string
	binaries  []string
	measured  bool
}

// runningLaunch is a started bootstrap and the facts its desktop reported.
type runningLaunch struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	facts  protectedFacts
	result *launchWaitResult
	stderr *strings.Builder
}

type launchWaitResult struct {
	done chan struct{}
	err  error
}

func (launch runningLaunch) wait() error { <-launch.result.done; return launch.result.err }

func startLaunch(t *testing.T, f launchFixture, env []string, args ...string) runningLaunch {
	t.Helper()
	cmd := exec.Command(f.bootstrap, args...)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	testutil.FailErr(t, "desktop input", err)
	stdout, err := cmd.StdoutPipe()
	testutil.FailErr(t, "desktop report", err)
	reader := bufio.NewReader(stdout)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stopProbe := make(chan struct{})
	probeErr := make(chan error, 1)
	var bootstrap atomic.Int32
	go probeEarlyBrokerAgent(&bootstrap, stopProbe, probeErr)
	// Registered before the wait below so it runs after the bootstrap has
	// exited and its standard error is complete.
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("bootstrap stderr:\n%s", stderr.String())
		}
	})
	testutil.FailErr(t, "start bootstrap", cmd.Start())
	bootstrap.Store(int32(cmd.Process.Pid))
	result := &launchWaitResult{done: make(chan struct{})}
	go func() { result.err = cmd.Wait(); close(result.done) }()
	t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case <-result.done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-result.done
		}
	})
	lines := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
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
	return runningLaunch{cmd: cmd, stdin: stdin, reader: reader, facts: facts, result: result, stderr: &stderr}
}

// processState reads the kernel's State line for pid.
func processState(t *testing.T, pid int) string {
	t.Helper()
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	testutil.FailErr(t, "process status", err)
	for _, line := range strings.Split(string(raw), "\n") {
		if state, ok := strings.CutPrefix(line, "State:"); ok {
			return strings.TrimSpace(state)
		}
	}
	t.Fatalf("no State line in /proc/%d/status", pid)
	return ""
}

// exitedState reports a zombie or dead State line.
func exitedState(state string) bool {
	return strings.HasPrefix(state, "Z") || strings.HasPrefix(state, "X")
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
		"PW_PROTECTED_LAUNCH=forged", "LD_PRELOAD=/tmp/planted.so", "GTK_MODULES=planted", "LYCAON_SANDBOX=off", "SSL_CERT_FILE=/tmp/planted.pem",
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
		if key != protectedLaunchEnv && !desktopEnvironmentKeys[key] && !strings.HasPrefix(key, "LC_") {
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
	if facts.ProtectedLaunch != "1" {
		t.Fatalf("verified entry marker = %q", facts.ProtectedLaunch)
	}
	selection, err := decodeEntry(facts.GenerationFrame)
	testutil.FailErr(t, "desktop generation selection", err)
	if selection.SupervisorPID != facts.BrokerPeerPID || selection.UID != uint32(uid) || selection.GID != uint32(gid) || selection.Generation.LogicalInstallRoot != f.install {
		t.Fatalf("desktop received a different authenticated selection: %+v", selection)
	}
	if len(facts.OpenBeyond) != 0 {
		t.Fatalf("bootstrap descriptors reached the desktop: %v", facts.OpenBeyond)
	}
	// The supervisor waits on the launch from before the desktop's first
	// broker request until the tree ends; one that exited left the desktop
	// with a broker nobody serves.
	if state := processState(t, launch.cmd.Process.Pid); exitedState(state) || exitedState(facts.SupervisorState) {
		t.Fatalf("supervisor %d is %q now and was %q at the desktop's broker probe, with %d broker agents", launch.cmd.Process.Pid, state, facts.SupervisorState, facts.BrokerAgents)
	}
	assertBootstrapSupervisorProfile(t, launch.cmd.Process.Pid)
	agentPID := findBrokerAgentPID(t, launch.cmd.Process.Pid)
	assertProcessThreadCredentials(t, agentPID, "0", 1)
	assertBrokerAgentSealed(t, agentPID)
	assertProcessThreadCredentials(t, facts.PID, "1", 5)
	// The broker starts programs outside the protected namespace, with the
	// caller's ordinary authority, and ends one whose caller abandons it.
	own, err := os.Readlink("/proc/self/ns/user")
	testutil.FailErr(t, "own user namespace", err)
	if facts.BrokerError != "" || facts.BrokerExit != 0 || !strings.Contains(facts.BrokerOutput, own) || !strings.Contains(facts.BrokerOutput, "NoNewPrivs:\t0") {
		t.Fatalf("brokered program did not run in the caller's namespace without no_new_privs: %q exit %d error %q (want %s); supervisor %q with %d broker agents at the probe", facts.BrokerOutput, facts.BrokerExit, facts.BrokerError, own, facts.SupervisorState, facts.BrokerAgents)
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
	// Native code maps executable only from the administrator's closure; the
	// caller's own files stay executable for programs the broker starts.
	if facts.ModuleUser != "EPERM" || facts.ModuleLibrary != "ok" {
		t.Fatalf("module view: caller file %s, library root %s", facts.ModuleUser, facts.ModuleLibrary)
	}
	if facts.BrokerUserExec != "brokered-user-program" {
		t.Fatalf("brokered program from the caller's files: %q", facts.BrokerUserExec)
	}
	finishObservation, checkpointObservation := observeFixtureLifecycle(t, f, launch)
	defer finishObservation()
	mountErr := sudoFixture(t, "module-view", f.dir, fmt.Sprintf("%s=%d", fixtureDesktopPIDEnv, facts.PID),
		fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()),
		"PW_FIXTURE_DESKTOP_START="+strconv.FormatUint(facts.ProcessStarts[facts.PID], 10),
		"PW_FIXTURE_BOOTSTRAP_PID="+strconv.Itoa(launch.cmd.Process.Pid))
	checkpointObservation()
	if mountErr != nil {
		t.Errorf("post-launch host mounts arrive noexec in the module view: %v", mountErr)
		t.Logf("failed_setup_cleanup_stdin_close_begin_monotonic_ns=%d", lifecycleMonotonicNS())
		_ = launch.stdin.Close()
		t.Logf("failed_setup_cleanup_stdin_close_end_monotonic_ns=%d", lifecycleMonotonicNS())
		waitLaunchEnd(t, launch)
		t.Logf("settled failed-setup bootstrap stderr: %s", launch.stderr.String())
		return
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
		t.Error("the generation lifetime lock was free while the desktop ran")
	}
	t.Logf("desktop_input_close_begin_monotonic_ns=%d", lifecycleMonotonicNS())
	if err := launch.stdin.Close(); err != nil {
		t.Errorf("close desktop input: %v", err)
	}
	t.Logf("desktop_input_close_end_monotonic_ns=%d", lifecycleMonotonicNS())
	waitLaunchEnd(t, launch)
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

// assertProcessThreadCredentials requires every thread of pid in /proc/<pid>/task/*/status
// to hold only the caller's identity, zero capabilities, and the expected NoNewPrivs flag.
func assertProcessThreadCredentials(t *testing.T, pid int, wantNoNewPrivs string, minThreads int) {
	t.Helper()
	groups, err := unix.Getgroups()
	testutil.FailErr(t, "caller groups", err)
	threads, err := verifyProcessThreadsStatus(pid, os.Getuid(), os.Getgid(), groups, wantNoNewPrivs)
	testutil.FailErr(t, "process thread credentials", err)
	if threads < minThreads {
		t.Fatalf("process %d checked %d threads, want at least %d", pid, threads, minThreads)
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
	runtime.LockOSThread()
	if err := prctlAllThreads(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		os.Exit(3)
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
func waitLaunchEnd(t *testing.T, launch runningLaunch) {
	t.Helper()
	select {
	case <-launch.result.done:
		if err := launch.result.err; err != nil {
			t.Fatalf("supervisor exit: %v; stderr: %s", err, launch.stderr.String())
		}
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
		t.Fatalf("the fixture engine reported no launches: %+v; bootstrap stderr: %s", launch.facts, launch.stderr.String())
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
			t.Fatalf("%s launch did not run outside the protected namespace with its exit status: %q code %d error %q (want %s); bootstrap stderr: %s", name, run.output, run.code, run.err, initial, launch.stderr.String())
		}
	}
	if probe.ConfinedError != "" && !strings.Contains(probe.ConfinedError, "exit status") || strings.Contains(probe.ConfinedOutput, initial) || strings.Contains(probe.ConfinedOutput, protected) || !strings.Contains(probe.ConfinedOutput, "NoNewPrivs:\t1") {
		t.Fatalf("confined launch did not run in its own nested namespace: %q code %d error %q stderr %q; bootstrap stderr: %s", probe.ConfinedOutput, probe.ConfinedCode, probe.ConfinedError, probe.ConfinedStderr, launch.stderr.String())
	}
	if probe.PreparedError != "" || probe.PreparedCode != 7 || !strings.Contains(probe.PreparedOutput, "streamed-over-stdin") || !strings.Contains(probe.PreparedOutput, initial) || !strings.Contains(probe.PreparedOutput, "NoNewPrivs:\t0") {
		t.Fatalf("prepared launch did not stream through the broker outside the protected namespace: %q code %d error %q (want %s); bootstrap stderr: %s", probe.PreparedOutput, probe.PreparedCode, probe.PreparedError, initial, launch.stderr.String())
	}
	if probe.PreparedConfinedError != "" || probe.PreparedConfinedCode != 7 || !strings.Contains(probe.PreparedConfinedOutput, "streamed-over-stdin") || strings.Contains(probe.PreparedConfinedOutput, initial) || strings.Contains(probe.PreparedConfinedOutput, protected) || !strings.Contains(probe.PreparedConfinedOutput, "NoNewPrivs:\t1") {
		t.Fatalf("prepared confined launch did not run in its own nested namespace through the broker: %q code %d error %q; bootstrap stderr: %s", probe.PreparedConfinedOutput, probe.PreparedConfinedCode, probe.PreparedConfinedError, launch.stderr.String())
	}
	if !probe.TerminalSurvive {
		t.Fatalf("a brokered terminal program did not survive Ctrl-C: %s; bootstrap stderr: %s", probe.TerminalError, launch.stderr.String())
	}
	if !probe.RelayLossEnded {
		t.Fatalf("killing the relay did not end the brokered session: %s; bootstrap stderr: %s", probe.RelayLossError, launch.stderr.String())
	}
	if !probe.BrowserEngineLossEnded {
		t.Fatalf("killing the protected engine did not end the brokered browser: %s; bootstrap stderr: %s", probe.BrowserEngineLossError, launch.stderr.String())
	}
	testutil.FailErr(t, "close desktop input", launch.stdin.Close())
	waitLaunchEnd(t, launch)
}

func assertBootstrapSupervisorProfile(t *testing.T, pid int) {
	t.Helper()
	groups, err := os.Getgroups()
	testutil.FailErr(t, "capture supervisor caller groups", err)
	caller := Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	for _, group := range groups {
		caller.Groups = append(caller.Groups, uint32(group))
	}
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	testutil.FailErr(t, "read live caller supervisor threads", err)
	if len(tasks) == 0 {
		t.Fatal("caller supervisor has no threads")
	}
	for _, task := range tasks {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/status", pid, task.Name()))
		testutil.FailErr(t, "read caller supervisor authority", err)
		testutil.FailErr(t, "caller supervisor fully dropped capabilities and bounding set", guardianThreadProfile(raw, caller))
	}
}
