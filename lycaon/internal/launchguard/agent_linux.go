//go:build linux

package launchguard

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchbroker"
)

// BrokerAgentArgument selects the entry image's broker agent mode.
const BrokerAgentArgument = "--broker-agent"

// Descriptors the privileged stage hands the broker agent.
const (
	agentSpecFD   = 3
	agentBrokerFD = 4
)

// RunBrokerAgent serves the launch broker: the caller's identity in the
// initial user namespace, inside the launch's mount namespace, non-dumpable
// from execve through shutdown and with no no_new_privs, so brokered programs
// keep every ordinary user facility, set-ID programs included, and see the
// running generation at the logical installation root.
func RunBrokerAgent() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.CloseRange(agentBrokerFD+1, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	return runBrokerAgent(
		os.NewFile(agentSpecFD, "sealed-entry-spec"),
		os.NewFile(agentBrokerFD, "launch-broker"),
	)
}

func runBrokerAgent(frame, broker *os.File) error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	spec, err := readEntry(frame)
	_ = frame.Close()
	if err != nil {
		_ = broker.Close()
		return err
	}
	if err := sealBrokerAgentIdentity(spec); err != nil {
		_ = broker.Close()
		return err
	}
	if err := initialUserNamespace(); err != nil {
		_ = broker.Close()
		return err
	}
	if peer, err := launchbroker.CheckSocket(int(broker.Fd())); err != nil || peer.Uid != trustedOwner || int(peer.Pid) != spec.SupervisorPID {
		_ = broker.Close()
		return errors.New("broker agent socket is not the launch supervisor")
	}
	unix.CloseOnExec(int(broker.Fd()))
	agent := &brokerAgent{calls: map[int]*brokerCall{}}
	agent.serve(broker)
	return nil
}

// sealBrokerAgentIdentity drops from the stage's administrator identity to the
// original caller while keeping the broker agent non-dumpable across the
// transition.
func sealBrokerAgentIdentity(spec EntrySpec) error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	uid, euid, suid := unix.Getresuid()
	gid, egid, sgid := unix.Getresgid()
	if uid == 0 && euid == 0 && suid == 0 && gid == 0 && egid == 0 && sgid == 0 {
		if err := dropPrivileges(Identity{UID: spec.UID, GID: spec.GID, Groups: spec.Groups}); err != nil {
			return err
		}
	}
	if err := clearCapabilities(); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	if dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0); err != nil || dumpable != 0 {
		return errors.New("broker agent is dumpable")
	}
	return sameIdentity(spec)
}

// initialUserNamespace requires this process to be in the initial user
// namespace.
func initialUserNamespace() error {
	own, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	defer func() { _ = own.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(int(own.Fd()), &stat); err != nil {
		return err
	}
	// The kernel numbers the initial user namespace PROC_USER_INIT_INO.
	if stat.Ino != 0xEFFFFFFD {
		return errors.New("broker agent is not in the initial user namespace")
	}
	return nil
}
