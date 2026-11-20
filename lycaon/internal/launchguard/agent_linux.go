//go:build linux

package launchguard

import (
	"errors"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchbroker"
)

// BrokerAgentArgument selects the entry image's broker agent mode.
const BrokerAgentArgument = "--broker-agent"

// Fixed descriptors the privileged stage hands the ordinary broker agent.
const (
	agentSpecFD   = 3
	agentBrokerFD = 4
	agentWatchFD  = 5
)

// RunBrokerAgent serves the launch broker: the caller's identity in the
// initial user namespace, inside the launch's mount namespace, non-dumpable
// from execve through shutdown and with no no_new_privs, so brokered programs
// keep every ordinary user facility, set-ID programs included, and see the
// running generation at the logical installation root.
func RunBrokerAgent() error {
	// Reject unrelated runtime descriptors before taking ownership of the inherited frame.
	if seals, err := unix.FcntlInt(agentSpecFD, unix.F_GET_SEALS, 0); err != nil || seals&entrySeals != entrySeals {
		return unsealedEntry(agentSpecFD, seals, err)
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}

	unix.CloseOnExec(agentWatchFD)
	if err := unix.CloseRange(agentWatchFD+1, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	return runBrokerAgent(
		os.NewFile(agentSpecFD, "sealed-entry-spec"),
		os.NewFile(agentBrokerFD, "launch-broker"),
		os.NewFile(agentSelfFD+1, "broker-caller-readiness"),
	)
}

func runBrokerAgent(frame, broker, ready *os.File) error {
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
	if ready == nil {
		return errors.New("broker agent lacks caller readiness pipe")
	}
	var readyStat unix.Stat_t
	if err := unix.Fstat(int(ready.Fd()), &readyStat); err != nil || readyStat.Mode&unix.S_IFMT != unix.S_IFIFO || readyStat.Uid != trustedOwner {
		return errors.New("broker readiness is not the stage pipe")
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		return err
	}
	_ = ready.Close()
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

const watcherCapabilityMask = (uint32(1) << unix.CAP_SYS_ADMIN) | (uint32(1) << unix.CAP_DAC_READ_SEARCH)

func watcherUserNamespace() error {
	own, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	defer func() { _ = own.Close() }()
	var ownStat, ownerStat unix.Stat_t
	if err := unix.Fstat(int(own.Fd()), &ownStat); err != nil {
		return err
	}
	if ownStat.Ino == 0xEFFFFFFD {
		return errors.New("module view watcher must not run in the initial user namespace")
	}
	if owner, err := unix.IoctlGetInt(int(own.Fd()), unix.NS_GET_OWNER_UID); err != nil || owner != 0 {
		return errors.New("module view watcher user namespace is not owned by the administrator")
	}
	// NS_GET_PARENT requires CAP_SYS_ADMIN in the parent user namespace; EPERM
	// proves the watcher holds no capabilities in the initial user namespace.
	if parentFD, err := unix.IoctlRetInt(int(own.Fd()), unix.NS_GET_PARENT); !errors.Is(err, unix.EPERM) {
		if err == nil {
			_ = unix.Close(parentFD)
		}
		return errors.New("module view watcher unexpectedly holds capabilities in its parent user namespace")
	}
	mnt, err := os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		return err
	}
	defer func() { _ = mnt.Close() }()
	ownerFD, err := unix.IoctlRetInt(int(mnt.Fd()), unix.NS_GET_USERNS)
	if err != nil {
		return err
	}
	err = unix.Fstat(ownerFD, &ownerStat)
	_ = unix.Close(ownerFD)
	if err != nil || ownerStat.Ino != ownStat.Ino {
		return errors.New("module view watcher user namespace does not own the module view")
	}
	return nil
}

// sealModuleWatcher switches every OS thread of the watcher to the caller's
// UID, GID and supplementary groups so FUSE mounts without allow_other permit
// path lookup, while keeping only CAP_SYS_ADMIN and CAP_DAC_READ_SEARCH in
// CapEff, CapPrm and CapBnd across all threads with no ambient or inheritable
// capabilities, no_new_privs and non-dumpable state.
func sealModuleWatcher(caller Identity) error {
	if err := dropWatcherBoundingSet(); err != nil {
		return err
	}
	if err := prctlAllThreads(unix.PR_SET_KEEPCAPS, 1, 0, 0, 0); err != nil {
		return err
	}
	groups := make([]int, len(caller.Groups))
	for i, group := range caller.Groups {
		groups[i] = int(group)
	}
	if err := syscall.Setgroups(groups); err != nil {
		return err
	}
	if err := unix.Setresgid(int(caller.GID), int(caller.GID), int(caller.GID)); err != nil {
		return err
	}
	if err := unix.Setresuid(int(caller.UID), int(caller.UID), int(caller.UID)); err != nil {
		return err
	}
	if err := prctlAllThreads(unix.PR_SET_KEEPCAPS, 0, 0, 0, 0); err != nil {
		return err
	}
	if err := prctlAllThreads(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return err
	}
	if err := prctlAllThreads(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	capabilities := [2]unix.CapUserData{{Permitted: watcherCapabilityMask, Effective: watcherCapabilityMask}}
	// #nosec G103 -- both pointers name fixed-layout capability structures copied synchronously by the kernel.
	if _, _, errno := syscall.AllThreadsSyscall(
		unix.SYS_CAPSET,
		uintptr(unsafe.Pointer(&header)),
		uintptr(unsafe.Pointer(&capabilities[0])),
		0,
	); errno != 0 {
		return errno
	}
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}

func dropWatcherBoundingSet() error {
	for cap := uintptr(0); cap <= 63; cap++ {
		if cap == unix.CAP_SYS_ADMIN || cap == unix.CAP_DAC_READ_SEARCH {
			continue
		}
		if err := prctlAllThreads(unix.PR_CAPBSET_DROP, cap, 0, 0, 0); err != nil {
			if errors.Is(err, unix.EINVAL) {
				break
			}
			return err
		}
	}
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
