//go:build linux

package linuxsandbox

import (
	"fmt"
	"unsafe"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// seccomp_data field offsets.
const (
	seccompDataNr   = 0
	seccompDataArch = 4
	seccompDataArg0 = 16
	// seccompDataArg1Low is the low half of the second argument on the
	// little-endian architectures the helper builds for.
	seccompDataArg1Low = 24
)

// terminalInjectionRequests are the ioctl requests that push input into a
// terminal or drive the console. The kernel compares an ioctl request as a
// 32-bit value, so the filter does too.
var terminalInjectionRequests = []uint32{unix.TIOCSTI, unix.TIOCLINUX}

// ioUringSyscalls answer ENOSYS: io_uring operations run without passing the
// filter, so a ring could create and connect the sockets refused below.
var ioUringSyscalls = []uint32{unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER}

// escapeSyscalls would let a process leave or reshape the boundary the
// namespaces, mounts, and Landlock built. Each one kills the process.
var escapeSyscalls = []uint32{
	unix.SYS_UNSHARE, unix.SYS_SETNS,
	unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_MOUNT_SETATTR,
	unix.SYS_OPEN_TREE, unix.SYS_MOVE_MOUNT, unix.SYS_FSOPEN, unix.SYS_FSCONFIG, unix.SYS_FSMOUNT, unix.SYS_FSPICK,
	unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV,
	unix.SYS_KEXEC_LOAD, unix.SYS_KEXEC_FILE_LOAD, unix.SYS_REBOOT,
	unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE,
	unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN,
	unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY,
	unix.SYS_SWAPON, unix.SYS_SWAPOFF,
}

// namespaceCloneFlags are the clone flags that would create a namespace.
const namespaceCloneFlags = unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWNET |
	unix.CLONE_NEWUTS | unix.CLONE_NEWIPC | unix.CLONE_NEWCGROUP | unix.CLONE_NEWTIME

// unixDatagramCreators create Unix-domain sockets. A datagram one, even a
// connected end of a pair, sends wherever a later call names a path, which the
// filter cannot read, so the filter refuses creating one.
var unixDatagramCreators = []uint32{unix.SYS_SOCKET, unix.SYS_SOCKETPAIR}

// notifiedSyscalls go to the supervisor, which performs each one itself on its
// own descriptor for the socket with its own copy of the address.
var notifiedSyscalls = []uint32{unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN}

// fastOpenSends are the send calls that could start a connection through
// TCP Fast Open, by syscall number and the argument holding their flags.
var fastOpenSends = []struct{ nr, flagsArg uint32 }{
	{unix.SYS_SENDTO, 3}, {unix.SYS_SENDMSG, 2}, {unix.SYS_SENDMMSG, 3},
}

// seccompArgLow is the offset of the low half of syscall argument n.
func seccompArgLow(n uint32) uint32 { return seccompDataArg0 + 8*n }

// seccompProgram builds the filter: kill on an escape syscall, a terminal
// injection ioctl, or a clone that asks for a namespace; hand connect, bind,
// and listen to the supervisor, since a network namespace does not isolate
// path sockets and the filter cannot read an address; refuse creating a
// Unix-domain datagram socket or pair, whose sends name a path the supervisor
// never sees, and any
// send that would open a connection by Fast Open; answer io_uring and clone3
// with ENOSYS so libraries fall back to calls the filter can read; allow
// everything else.
func seccompProgram() ([]bpf.RawInstruction, error) {
	kill := uint32(unix.SECCOMP_RET_KILL_PROCESS)
	insts := []bpf.Instruction{
		bpf.LoadAbsolute{Off: seccompDataArch, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: nativeAuditArch, SkipTrue: 1},
		bpf.RetConstant{Val: kill},
		bpf.LoadAbsolute{Off: seccompDataNr, Size: 4},
	}
	if x32SyscallBit != 0 {
		insts = append(insts,
			bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: x32SyscallBit, SkipFalse: 1},
			bpf.RetConstant{Val: kill},
		)
	}
	for _, nr := range escapeSyscalls {
		insts = append(insts,
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: nr, SkipFalse: 1},
			bpf.RetConstant{Val: kill},
		)
	}
	// ioctl: load the request, compare it with each injection request, and
	// either reach the kill or jump over it to allow. Any other syscall skips
	// the whole block with the syscall number still loaded.
	n := len(terminalInjectionRequests)
	insts = append(insts,
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.SYS_IOCTL, SkipFalse: uint8(n + 4)},
		bpf.LoadAbsolute{Off: seccompDataArg1Low, Size: 4},
	)
	for i, req := range terminalInjectionRequests {
		insts = append(insts, bpf.JumpIf{Cond: bpf.JumpEqual, Val: req, SkipTrue: uint8(n - i)})
	}
	insts = append(insts,
		bpf.Jump{Skip: 1},
		bpf.RetConstant{Val: kill},
		bpf.RetConstant{Val: unix.SECCOMP_RET_ALLOW},
	)
	enosys := uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.ENOSYS)
	u := len(ioUringSyscalls)
	for i, nr := range ioUringSyscalls {
		// A match skips the remaining compares to the ENOSYS return; the last
		// miss skips that return.
		var past uint8
		if i == u-1 {
			past = 1
		}
		insts = append(insts, bpf.JumpIf{Cond: bpf.JumpEqual, Val: nr, SkipTrue: uint8(u - 1 - i), SkipFalse: past})
	}
	eacces := uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EACCES)
	insts = append(insts, bpf.RetConstant{Val: enosys})
	for _, nr := range unixDatagramCreators {
		// The domain and type are ints compared on their low halves.
		insts = append(insts,
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: nr, SkipFalse: 7},
			bpf.LoadAbsolute{Off: seccompDataArg0, Size: 4},
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.AF_UNIX, SkipFalse: 4},
			bpf.LoadAbsolute{Off: seccompArgLow(1), Size: 4},
			bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: socketTypeMask},
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.SOCK_DGRAM, SkipFalse: 1},
			bpf.RetConstant{Val: eacces},
			bpf.RetConstant{Val: unix.SECCOMP_RET_ALLOW},
		)
	}
	m := len(notifiedSyscalls)
	for i, nr := range notifiedSyscalls {
		var past uint8
		if i == m-1 {
			past = 1
		}
		insts = append(insts, bpf.JumpIf{Cond: bpf.JumpEqual, Val: nr, SkipTrue: uint8(m - 1 - i), SkipFalse: past})
	}
	insts = append(insts, bpf.RetConstant{Val: unix.SECCOMP_RET_USER_NOTIF})
	for _, send := range fastOpenSends {
		insts = append(insts,
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: send.nr, SkipFalse: 4},
			bpf.LoadAbsolute{Off: seccompArgLow(send.flagsArg), Size: 4},
			bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: unix.MSG_FASTOPEN, SkipFalse: 1},
			bpf.RetConstant{Val: eacces},
			bpf.RetConstant{Val: unix.SECCOMP_RET_ALLOW},
		)
	}
	insts = append(insts,
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.SYS_CLONE3, SkipFalse: 1},
		bpf.RetConstant{Val: enosys},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.SYS_CLONE, SkipFalse: 3},
		bpf.LoadAbsolute{Off: seccompDataArg0, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: namespaceCloneFlags, SkipFalse: 1},
		bpf.RetConstant{Val: kill},
		bpf.RetConstant{Val: unix.SECCOMP_RET_ALLOW},
	)
	return bpf.Assemble(insts)
}

// socketTypeMask isolates the socket type from the flags socket() accepts.
const socketTypeMask = 0xf

// applySeccomp installs the filter on every thread of the process and returns
// the descriptor its notifications arrive on.
func applySeccomp() (int, error) {
	raw, err := seccompProgram()
	if err != nil {
		return -1, fmt.Errorf("seccomp assemble: %w", err)
	}
	filter := make([]unix.SockFilter, len(raw))
	for i, inst := range raw {
		filter[i] = unix.SockFilter{Code: inst.Op, Jt: inst.Jt, Jf: inst.Jf, K: inst.K}
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	flags := unix.SECCOMP_FILTER_FLAG_TSYNC | unix.SECCOMP_FILTER_FLAG_TSYNC_ESRCH | unix.SECCOMP_FILTER_FLAG_NEW_LISTENER
	listener, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, uintptr(flags), uintptr(unsafe.Pointer(&prog)))
	if errno != 0 {
		return -1, fmt.Errorf("seccomp filter: %w", errno)
	}
	return int(listener), nil
}

// seccompMode is the process's seccomp mode: 2 once a filter is installed.
func seccompMode() (int, error) {
	return unix.PrctlRetInt(unix.PR_GET_SECCOMP, 0, 0, 0, 0)
}
