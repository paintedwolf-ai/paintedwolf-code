//go:build linux

package launchguard

import (
	"errors"
	"os"
	"runtime"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchbroker"
)

// BrokerAgentArgument selects the entry image's broker agent mode.
const BrokerAgentArgument = "--broker-agent"

// Descriptors the privileged stage hands the broker agent and module-view watcher.
const (
	agentSpecFD       = 3
	agentBrokerFD     = 4
	agentWatchFD      = 5
	watcherLifetimeFD = 4
	watcherReadyFD    = 5
	watcherSelfFD     = 6
)

// RunBrokerAgent serves the launch broker: the caller's identity in the
// initial user namespace, inside the launch's mount namespace, with no
// no_new_privs, so brokered programs keep every ordinary user facility,
// set-ID programs included, and see the running generation at the logical
// installation root. Started as uid 0 inside the module view, it runs the
// module-view mount watcher instead.
func RunBrokerAgent() error {
	frame := os.NewFile(agentSpecFD, "sealed-entry-spec")
	spec, err := readEntry(frame)
	_ = frame.Close()
	if err != nil {
		return err
	}
	if uid, euid, suid := unix.Getresuid(); uid == 0 && euid == 0 && suid == 0 {
		return runModuleViewWatcher(spec)
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
	unix.CloseOnExec(agentWatchFD)
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

// runModuleViewWatcher runs inside the module view in the initial user
// namespace as the caller's identity with only CAP_SYS_ADMIN and
// CAP_DAC_READ_SEARCH retained, marking every mount propagated into the view
// after launch noexec, nosuid and nodev until the launch ends.
func runModuleViewWatcher(spec EntrySpec) error {
	runtime.LockOSThread()
	if gid, egid, sgid := unix.Getresgid(); gid != 0 || egid != 0 || sgid != 0 {
		return errors.New("module view watcher requires administrator group identity")
	}
	if err := initialUserNamespace(); err != nil {
		return err
	}
	if err := freshMountNamespace(); err != nil {
		return err
	}
	for _, fd := range []int{watcherLifetimeFD, watcherReadyFD} {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFIFO {
			return errors.New("invalid module view watcher pipe")
		}
		unix.CloseOnExec(fd)
	}
	_ = unix.Close(watcherSelfFD)
	roots, err := allowedModuleMountPoints(spec.Generation)
	if err != nil {
		return err
	}
	mountinfo, err := os.Open("/proc/thread-self/mountinfo")
	if err != nil {
		return err
	}
	defer func() { _ = mountinfo.Close() }()
	if err := sealModuleWatcher(Identity{UID: spec.UID, GID: spec.GID, Groups: spec.Groups}); err != nil {
		return err
	}
	return watchModuleMounts(int(mountinfo.Fd()), watcherLifetimeFD, watcherReadyFD, spec.Generation.LogicalInstallRoot, roots)
}

// sealModuleWatcher switches the watcher to the caller's UID, GID and groups
// so FUSE mounts without allow_other permit path lookup, while keeping only
// CAP_SYS_ADMIN and CAP_DAC_READ_SEARCH on the locked thread with
// no_new_privs and non-dumpable state.
func sealModuleWatcher(caller Identity) error {
	if err := unix.Prctl(unix.PR_SET_KEEPCAPS, 1, 0, 0, 0); err != nil {
		return err
	}
	groups := make([]int, len(caller.Groups))
	for i, group := range caller.Groups {
		groups[i] = int(group)
	}
	if err := unix.Setgroups(groups); err != nil {
		return err
	}
	if err := unix.Setresgid(int(caller.GID), int(caller.GID), int(caller.GID)); err != nil {
		return err
	}
	if err := unix.Setresuid(int(caller.UID), int(caller.UID), int(caller.UID)); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_KEEPCAPS, 0, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return err
	}
	const mask = (uint32(1) << unix.CAP_SYS_ADMIN) | (uint32(1) << unix.CAP_DAC_READ_SEARCH)
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	capabilities := [2]unix.CapUserData{{Permitted: mask, Effective: mask}}
	if err := unix.Capset(&header, &capabilities[0]); err != nil {
		return err
	}
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
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
