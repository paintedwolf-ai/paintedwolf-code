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
// initial user namespace, inside the launch's mount namespace, with no
// no_new_privs, so brokered programs keep every ordinary user facility,
// set-ID programs included, and see the running generation at the logical
// installation root.
func RunBrokerAgent() error {
	frame := os.NewFile(agentSpecFD, "sealed-entry-spec")
	spec, err := readEntry(frame)
	_ = frame.Close()
	if err != nil {
		return err
	}
	if err := sameIdentity(spec); err != nil {
		return err
	}
	if err := initialUserNamespace(); err != nil {
		return err
	}
	broker := os.NewFile(agentBrokerFD, "launch-broker")
	if _, err := launchbroker.CheckSocket(agentBrokerFD); err != nil {
		return err
	}
	unix.CloseOnExec(agentBrokerFD)
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return err
	}
	agent := &brokerAgent{calls: map[int]*brokerCall{}}
	agent.serve(broker)
	return nil
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
