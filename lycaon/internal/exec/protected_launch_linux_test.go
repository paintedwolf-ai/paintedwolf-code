//go:build linux

package exec

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/launchbroker"
	"github.com/lycaon/lycaon/internal/testutil"
)

func protectedFacts() (userNamespaceFacts, error) {
	return userNamespaceFacts{owner: 0, nested: true, identity: true}, nil
}

func TestProtectedLaunchIsDecidedByNamespaceFacts(t *testing.T) {
	for name, facts := range map[string]userNamespaceFacts{
		"initial namespace":   {owner: 0, nested: false, identity: true},
		"caller-owned nested": {owner: 1000, nested: true, identity: true},
		"remapped nested":     {owner: 0, nested: true, identity: false},
	} {
		state := detectProtectedLaunch(func() (userNamespaceFacts, error) { return facts, nil }, "6")
		if state.protected || state.err != nil || state.broker != nil {
			t.Fatalf("%s treated as the protected launch: %+v", name, state)
		}
	}
	if state := detectProtectedLaunch(func() (userNamespaceFacts, error) { return userNamespaceFacts{}, errors.New("unreadable") }, ""); state.protected || state.err == nil {
		t.Fatalf("unreadable facts: %+v", state)
	}
}

func TestProtectedLaunchRefusesWithoutTheBootstrapsBroker(t *testing.T) {
	if state := detectProtectedLaunch(protectedFacts, ""); !state.protected || !errors.Is(state.err, ErrLaunchBrokerUnavailable) {
		t.Fatalf("missing broker: %+v", state)
	}
	pipe, write, err := os.Pipe()
	testutil.FailErr(t, "pipe", err)
	defer func() { _ = pipe.Close(); _ = write.Close() }()
	if state := detectProtectedLaunch(protectedFacts, strconv.Itoa(int(pipe.Fd()))); !errors.Is(state.err, ErrLaunchBrokerUnavailable) {
		t.Fatalf("a pipe accepted as the broker: %+v", state)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "socket pair", err)
	defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
	state := detectProtectedLaunch(protectedFacts, strconv.Itoa(pair[0]))
	if os.Geteuid() != 0 && !errors.Is(state.err, ErrLaunchBrokerUnavailable) {
		t.Fatalf("a socket pair this process created accepted as the bootstrap's broker: %+v", state)
	}
	for _, named := range []string{"0", "2", "x", "-1"} {
		if state := detectProtectedLaunch(protectedFacts, named); !errors.Is(state.err, ErrLaunchBrokerUnavailable) {
			t.Fatalf("descriptor %q accepted: %+v", named, state)
		}
	}
}

func TestRelayedSupervisorsStaySupervised(t *testing.T) {
	supervised := &exec.Cmd{Args: []string{"/opt/painted-wolf-code/bin/pw", supervisorCommand, "0", "normal", "/usr/bin/git", "git"}}
	relayed := &exec.Cmd{Args: append([]string{"/opt/painted-wolf-code/bin/pw", relayCommand}, supervised.Args[1:]...)}
	if !commandSupervisor(supervised) || !commandSupervisor(relayed) || relayedCommand(supervised) || !relayedCommand(relayed) {
		t.Fatal("relay and supervisor commands are not recognised")
	}
	if commandSupervisor(&exec.Cmd{Args: []string{"pw", relayCommand, "git"}}) {
		t.Fatal("a relay of something other than a supervisor counted as supervised")
	}
	for _, sig := range []os.Signal{unix.SIGTERM, unix.SIGUSR2, unix.SIGINT} {
		if !slices.Contains(relayedSignals, sig) {
			t.Fatalf("the relay does not forward %v, which the guard sends supervisors", sig)
		}
	}
}

func TestOutsideTheProtectedLaunchCommandsAreNotRelayed(t *testing.T) {
	cmd := &exec.Cmd{Path: "/bin/sh", Args: []string{"/bin/sh", supervisorCommand}}
	if protectedState().protected {
		t.Fatal("the exec suite must run outside a protected launch")
	}
	testutil.FailErr(t, "relay outside protection", relayThroughBroker(cmd))
	if relayedCommand(cmd) || len(cmd.ExtraFiles) != 0 {
		t.Fatalf("command relayed outside the protected launch: %q", cmd.Args)
	}
}

// Inside the protected launch every supervisor and every prepared command
// becomes a relay, a confinement helper's included; a missing broker refuses
// instead, and supervising a prepared command replaces its relay rather than
// nesting two.
func TestEveryLaunchIsRelayedInsideTheProtectedLaunch(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "socket pair", err)
	broker := os.NewFile(uintptr(pair[0]), "broker")
	defer func() { _ = broker.Close(); _ = unix.Close(pair[1]) }()
	state := protectedLaunch{protected: true, broker: broker}
	for name, target := range map[string][]string{
		"unconfined":         {"/usr/bin/git", "git", "status"},
		"confinement helper": {"/opt/painted-wolf-code/bin/pw", "--confine-helper", "--", "/usr/bin/git"},
	} {
		lifetime, status, err := os.Pipe()
		testutil.FailErr(t, "pipe", err)
		cmd := &exec.Cmd{Path: "/opt/painted-wolf-code/bin/pw", Args: append([]string{"/opt/painted-wolf-code/bin/pw", supervisorCommand, "0", "normal"}, target...), ExtraFiles: []*os.File{lifetime, status}}
		testutil.FailErr(t, "relay "+name, relayThrough(cmd, state))
		if !relayedCommand(cmd) || cmd.Args[2] != supervisorCommand || cmd.ExtraFiles[len(cmd.ExtraFiles)-1] != broker {
			t.Fatalf("%s launch not relayed with the broker last: %q", name, cmd.Args)
		}
		extras, argv, ok := parseRelayTarget("/opt/painted-wolf-code/bin/pw", cmd.Args[2:])
		if !ok || extras != 2 || len(argv) < 2 || argv[1] != supervisorCommand {
			t.Fatalf("%s supervisor relay target = (%d, %q, %v)", name, extras, argv, ok)
		}
		_ = lifetime.Close()
		_ = status.Close()
	}
	profileRead, profileWrite, err := os.Pipe()
	testutil.FailErr(t, "profile pipe", err)
	defer func() { _ = profileRead.Close(); _ = profileWrite.Close() }()
	prepared := &exec.Cmd{
		Path:       "/opt/painted-wolf-code/bin/pw",
		Args:       []string{"/opt/painted-wolf-code/bin/pw", "--confine-helper", "-profile-fd", "3", "--", "/usr/bin/git", "status"},
		ExtraFiles: []*os.File{profileRead},
	}
	testutil.FailErr(t, "relay prepared command", relayThrough(prepared, state))
	if !relayedCommand(prepared) || commandSupervisor(prepared) || prepared.ExtraFiles[len(prepared.ExtraFiles)-1] != broker {
		t.Fatalf("prepared command not relayed with broker last: %q", prepared.Args)
	}
	extras, argv, ok := parseRelayTarget("/opt/painted-wolf-code/bin/pw", prepared.Args[2:])
	if !ok || extras != 1 || !slices.Equal(argv, []string{"/opt/painted-wolf-code/bin/pw", "--confine-helper", "-profile-fd", "3", "--", "/usr/bin/git", "status"}) {
		t.Fatalf("prepared relay target = (%d, %q, %v)", extras, argv, ok)
	}
	unrelayCommand(prepared)
	if relayedCommand(prepared) || prepared.Path != "/opt/painted-wolf-code/bin/pw" || len(prepared.ExtraFiles) != 1 || prepared.ExtraFiles[0] != profileRead {
		t.Fatalf("unrelayCommand did not restore prepared command: path=%q args=%q extra=%d", prepared.Path, prepared.Args, len(prepared.ExtraFiles))
	}
	unrelayCommand(prepared)
	unrelayCommand(&exec.Cmd{Args: []string{"pw", relayCommand, "bad", "/bin/sh", "sh"}})
	events := make(chan os.Signal, 4)
	notifySupervisorSignals(events, true)()
	notifySupervisorSignals(events, false)()
	if code := runBrokerRelay(nil); code != 125 {
		t.Fatalf("runBrokerRelay(nil) = %d, want 125", code)
	}
	if code := runBrokerRelay([]string{"0", "/bin/true", "true"}); code != 125 {
		t.Fatalf("runBrokerRelay without broker socket = %d, want 125", code)
	}
	for _, invalid := range [][]string{
		nil,
		{supervisorCommand, "bad"},
		{"-1", "/usr/bin/git", "git"},
		{"x", "/usr/bin/git", "git"},
		{"0", "", "git"},
	} {
		if _, _, ok := parseRelayTarget("/opt/painted-wolf-code/bin/pw", invalid); ok {
			t.Fatalf("parseRelayTarget(%q) succeeded", invalid)
		}
	}
	missing := protectedLaunch{protected: true, err: ErrLaunchBrokerUnavailable}
	if err := relayThrough(&exec.Cmd{Args: []string{"pw", supervisorCommand}}, missing); !errors.Is(err, ErrLaunchBrokerUnavailable) {
		t.Fatalf("a launch without the broker = %v, want refusal", err)
	}
	if _, err := StartInOwnGroup(t.Context(), &exec.Cmd{Path: "/nonexistent/binary", Args: []string{"binary"}}); err == nil {
		t.Fatal("StartInOwnGroup with missing binary succeeded")
	}

	callPair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "call socket pair", err)
	callClient := os.NewFile(uintptr(callPair[0]), "call-client")
	defer func() { _ = callClient.Close(); _ = unix.Close(callPair[1]) }()
	sigs := make(chan os.Signal, 2)
	sigs <- syscall.Signal(0)
	go func() {
		launchbroker.SendEvent(callPair[1], launchbroker.Event{Started: os.Getpid()})
		sigs <- syscall.Signal(0)
		launchbroker.SendEvent(callPair[1], launchbroker.Event{Exited: true, Code: 7})
	}()
	if code := relayEvents(callClient, sigs); code != 7 {
		t.Fatalf("relayEvents exit code = %d, want 7", code)
	}

	errPair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "error socket pair", err)
	errClient := os.NewFile(uintptr(errPair[0]), "err-client")
	defer func() { _ = errClient.Close(); _ = unix.Close(errPair[1]) }()
	launchbroker.SendEvent(errPair[1], launchbroker.Event{Error: "broker error"})
	if code := relayEvents(errClient, make(chan os.Signal)); code != 125 {
		t.Fatalf("relayEvents error code = %d, want 125", code)
	}

	eofPair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "eof socket pair", err)
	eofClient := os.NewFile(uintptr(eofPair[0]), "eof-client")
	_ = unix.Close(eofPair[1])
	defer func() { _ = eofClient.Close() }()
	if code := relayEvents(eofClient, make(chan os.Signal)); code != 125 {
		t.Fatalf("relayEvents eof code = %d, want 125", code)
	}
}

func TestBrokerRelaySubprocess(t *testing.T) {
	if os.Getenv("PW_TEST_BROKER_RELAY_SUBPROCESS") == "1" {
		if code := runBrokerRelay([]string{"1", "/bin/true", "true"}); code != 9 {
			t.Fatalf("runBrokerRelay = %d, want 9", code)
		}
		return
	}
	self, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "broker socket pair", err)
	brokerChild := os.NewFile(uintptr(pair[0]), "broker-child")
	brokerServer := os.NewFile(uintptr(pair[1]), "broker-server")
	defer func() { _ = brokerChild.Close(); _ = brokerServer.Close() }()

	done := make(chan error, 1)
	go func() {
		payload := make([]byte, launchbroker.MaxRequest+1)
		control := make([]byte, launchbroker.ControlSpace())
		n, oobn, _, _, err := unix.Recvmsg(int(brokerServer.Fd()), payload, control, unix.MSG_CMSG_CLOEXEC)
		if err != nil {
			done <- err
			return
		}
		_, fds, err := launchbroker.Decode(payload[:n], control[:oobn])
		if err != nil {
			done <- err
			return
		}
		for _, fd := range fds[1:] {
			_ = unix.Close(fd)
		}
		launchbroker.SendEvent(fds[0], launchbroker.Event{Started: os.Getpid()})
		launchbroker.SendEvent(fds[0], launchbroker.Event{Exited: true, Code: 9})
		_ = unix.Close(fds[0])
		done <- nil
	}()

	cmd := exec.Command(self, "-test.run=^TestBrokerRelaySubprocess$", "-test.v=false")
	cmd.Env = append(os.Environ(), "PW_TEST_BROKER_RELAY_SUBPROCESS=1")
	if coverDir := os.Getenv("PW_GO_CHILD_COVERDIR"); coverDir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverDir)
	}
	// Slot 0 (fd 3) is nil to exercise closed-slot padding; slot 1 (fd 4) is the broker.
	cmd.ExtraFiles = []*os.File{nil, brokerChild}
	testutil.FailErr(t, "run broker relay subprocess", cmd.Run())
	testutil.FailErr(t, "serve broker relay request", <-done)
}

func TestPreparedInputsSurviveBrokerAndSupervisorPrefixes(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create broker pair", err)
	broker := os.NewFile(uintptr(pair[0]), "broker")
	defer func() { _ = broker.Close(); _ = unix.Close(pair[1]) }()
	cmd, release, err := prepareNativeCommand(t.Context(), "/bin/true", nil, ExecOpts{Launch: HostLaunch("broker binding fixture")}, nil, false)
	testutil.FailErr(t, "prepare bound input", err)
	defer release()
	prefix := slices.Clone(cmd.ExtraFiles)
	state := protectedLaunch{protected: true, broker: broker}
	testutil.FailErr(t, "relay prepared input", relayThrough(cmd, state))
	testutil.FailErr(t, "validate relayed input", confine.ValidateProcessInputs(cmd))
	cmd, cleanup, err := superviseCommand(cmd, func() {}, ProcessPriorityNormal)
	testutil.FailErr(t, "supervise relayed input", err)
	defer cleanup()
	supervisorBroker := protectedState().broker
	if !relayedCommand(cmd) {
		testutil.FailErr(t, "relay supervised input", relayThrough(cmd, state))
		supervisorBroker = broker
	}
	if !relayedCommand(cmd) || !commandSupervisor(cmd) || len(cmd.ExtraFiles) != len(prefix)+3 || !slices.Equal(cmd.ExtraFiles[:len(prefix)], prefix) || cmd.ExtraFiles[len(cmd.ExtraFiles)-1] != supervisorBroker {
		t.Fatalf("supervisor relay changed descriptor prefix: args=%q files=%d", cmd.Args, len(cmd.ExtraFiles))
	}
	testutil.FailErr(t, "validate supervised relayed input", confine.ValidateProcessInputs(cmd))
}

// Inside the module view the caller's programs are not executable by the
// engine itself, so a bare name resolves by its execute bits and the broker
// starts it outside.
func TestRelayedProgramsResolveWithoutAskingTheModuleView(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "tool")
	testutil.FailErr(t, "write program", os.WriteFile(program, []byte("#!/bin/sh\n"), 0o755))
	data := filepath.Join(dir, "notes")
	testutil.FailErr(t, "write data", os.WriteFile(data, []byte("x"), 0o644))
	if found, err := relayedExecutable(program); err != nil || found != program {
		t.Fatalf("program with execute bits: %q %v", found, err)
	}
	for _, path := range []string{data, dir, filepath.Join(dir, "missing")} {
		if _, err := relayedExecutable(path); err == nil {
			t.Fatalf("%s resolved as a program", path)
		}
	}
	t.Setenv("PATH", dir)
	unresolved := func() *exec.Cmd {
		return &exec.Cmd{Path: "tool", Err: &exec.Error{Name: "tool", Err: exec.ErrNotFound}}
	}
	cmd := unresolved()
	resolveRelayedProgram(cmd, protectedLaunch{protected: true})
	if cmd.Path != program || cmd.Err != nil {
		t.Fatalf("relayed program resolved to %q, %v", cmd.Path, cmd.Err)
	}
	cmd = unresolved()
	resolveRelayedProgram(cmd, protectedLaunch{})
	if cmd.Path != "tool" || cmd.Err == nil {
		t.Fatal("a program was resolved outside the protected launch")
	}
}
