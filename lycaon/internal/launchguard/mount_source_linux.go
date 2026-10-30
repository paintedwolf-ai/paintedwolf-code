//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/lycaon/lycaon/internal/launchbroker"
	"golang.org/x/sys/unix"
)

// MountSourceArgument selects the bounded caller-credential startup opener.
const MountSourceArgument = "--mount-source-opener"

const (
	sourceSpecFD = 3 + iota
	sourceOriginFD
	sourceTopologyFD
	sourceSocketFD
	sourceLifetimeFD
	sourceSelfFD
)

// RunMountSource opens only mount roots selected by the privileged startup
// frame. Its temporary root-owned user namespace shields the credential
// transition from same-user tracing; it has no authority over the inherited
// initial-owned staging mount namespace.
func RunMountSource() error {
	runtime.LockOSThread()
	if uid, effective, saved := unix.Getresuid(); uid != 0 || effective != 0 || saved != 0 {
		return errors.New("mount source requires the stage credential transition")
	}
	if gid, effective, saved := unix.Getresgid(); gid != 0 || effective != 0 || saved != 0 {
		return errors.New("mount source requires the stage group transition")
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	frame := os.NewFile(sourceSpecFD, "sealed-entry-spec")
	spec, err := readEntry(frame)
	_ = frame.Close()
	if err != nil {
		return err
	}
	origin := os.NewFile(sourceOriginFD, "initial-user-namespace")
	defer func() { _ = origin.Close() }()
	peer, err := launchbroker.CheckSocket(sourceSocketFD)
	if err != nil || peer.Uid != 0 || int(peer.Pid) != os.Getppid() {
		return errors.New("mount source socket is not the privileged stage")
	}
	var lifetime unix.Stat_t
	if err := unix.Fstat(sourceLifetimeFD, &lifetime); err != nil || lifetime.Mode&unix.S_IFMT != unix.S_IFIFO {
		return errors.New("invalid mount source lifetime")
	}
	topology := os.NewFile(sourceTopologyFD, "sealed-source-topology")
	entries, namespace, err := readSourceTopology(topology)
	_ = topology.Close()
	if err != nil {
		return err
	}
	if err := sourceNamespaces(origin, namespace); err != nil {
		return err
	}
	_ = unix.Close(sourceSelfFD)
	if err := sealMountSource(spec); err != nil {
		return err
	}
	defer func() { _ = unix.Close(sourceSocketFD) }()
	defer func() { _ = unix.Close(sourceLifetimeFD) }()
	return serveMountSource(entries, sourceSocketFD, sourceLifetimeFD)
}

func sourceNamespaces(origin *os.File, selected sourceNamespaceIdentity) error {
	var initial unix.Stat_t
	if err := unix.Fstat(int(origin.Fd()), &initial); err != nil || initial.Ino != 0xEFFFFFFD {
		return errors.New("mount source lacks the initial user namespace")
	}
	if err := verifyNamespace(origin); err != nil {
		return err
	}
	mounts, err := os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		return err
	}
	defer func() { _ = mounts.Close() }()
	if kind, err := unix.IoctlRetInt(int(mounts.Fd()), unix.NS_GET_NSTYPE); err != nil || kind != unix.CLONE_NEWNS {
		return errors.New("mount source current namespace is not a mount namespace")
	}
	var actual unix.Stat_t
	if err := unix.Fstat(int(mounts.Fd()), &actual); err != nil || uint64(actual.Dev) != selected.device || actual.Ino != selected.inode {
		return errors.New("mount source staging namespace differs from the sealed selection")
	}
	return nil
}

func sealMountSource(spec EntrySpec) error {
	for capability := uintptr(0); capability <= 63; capability++ {
		if err := prctlAllThreads(unix.PR_CAPBSET_DROP, capability, 0, 0, 0); err != nil {
			if errors.Is(err, unix.EINVAL) {
				break
			}
			return err
		}
	}
	if err := sealBrokerAgentIdentity(spec); err != nil {
		return err
	}
	return prctlAllThreads(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
}

func serveMountSource(entries []mountEntry, socket, lifetime int) error {
	selected := make(map[uint64]string, len(entries))
	for _, entry := range entries {
		if entry.id <= 0 {
			return errors.New("invalid selected mount identity")
		}
		selected[uint64(entry.id)] = entry.mountPoint
	}
	socketPoll, err := guardianPollDescriptor(uintptr(socket))
	if err != nil {
		return err
	}
	lifetimePoll, err := guardianPollDescriptor(uintptr(lifetime))
	if err != nil {
		return err
	}
	for sequence := uint64(1); sequence <= uint64(len(entries))+1; sequence++ {
		fds := []unix.PollFd{{Fd: socketPoll, Events: unix.POLLIN}, {Fd: lifetimePoll, Events: unix.POLLIN | unix.POLLHUP}}
		if _, err := unix.Poll(fds, -1); err != nil {
			if errors.Is(err, unix.EINTR) {
				sequence--
				continue
			}
			return err
		}
		if fds[1].Revents != 0 {
			return io.EOF
		}
		request, unexpected, err := receiveMountTransfer(socket)
		if unexpected >= 0 {
			_ = unix.Close(unexpected)
		}
		if err != nil {
			return err
		}
		if request.sequence != sequence || unexpected >= 0 {
			return errors.New("invalid mount source request sequence or descriptor")
		}
		if request.kind == mountTransferReady {
			return sendMountTransfer(socket, mountTransfer{kind: mountTransferAck, sequence: sequence}, -1)
		}
		if request.kind != mountTransferOpen || selected[request.id] != request.path {
			return errors.New("mount source request is outside the sealed topology")
		}
		delete(selected, request.id)
		fd, err := openMountPoint(request.path)
		if errors.Is(err, unix.EACCES) {
			// Denial is a typed observation, never permission for a privileged
			// lookup. The stage separately proves any prune-only fallback.
			err = sendMountTransfer(socket, mountTransfer{kind: mountTransferDenied, sequence: sequence, id: request.id, path: request.path}, -1)
			if err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("open selected mount source: %w", err)
		}
		id, identityErr := mountID(fd)
		alias, aliasErr := os.Readlink(mountDescriptorPath(fd))
		if identityErr != nil || id != request.id || aliasErr != nil || alias != request.path {
			_ = unix.Close(fd)
			return errors.New("selected mount source changed before opening")
		}
		err = sendMountTransfer(socket, mountTransfer{kind: mountTransferAdd, sequence: sequence, id: request.id, path: request.path}, fd)
		_ = unix.Close(fd)
		if err != nil {
			return err
		}
	}
	return errors.New("mount source request bound exceeded")
}
