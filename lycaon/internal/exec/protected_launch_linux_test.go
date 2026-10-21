//go:build linux

package exec

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"

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
		facts := facts
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
