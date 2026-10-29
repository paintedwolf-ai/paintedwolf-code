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
	unix.SYS_SETNS,
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

// socketCreators create sockets. A Unix-domain datagram one, even a connected
// end of a pair, sends wherever a later call names a path, which the filter
// cannot read, so the filter refuses creating one.
var socketCreators = []uint32{unix.SYS_SOCKET, unix.SYS_SOCKETPAIR}

// isolatedSocketFamilies are the address families the sandbox's namespaces
// and supervisor bound. Other families, such as AF_VSOCK, which reaches the
// hypervisor host from any network namespace, answer EAFNOSUPPORT, as on a
// kernel without them.
var isolatedSocketFamilies = []uint32{unix.AF_INET, unix.AF_INET6, unix.AF_NETLINK}

// notifiedSyscalls go to the supervisor, which performs each one itself on its
// own descriptor for the socket with its own copy of the address.
var notifiedSyscalls = []uint32{unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN}

// pathCreators create, link, or move a name; the supervisor decides each one
// against the protected names and performs it inside the sandbox. openat2
// carries its flags in memory, so every call goes; openat and open go only
// when they would create.
var pathCreators = []uint32{
	unix.SYS_OPENAT2, unix.SYS_MKDIRAT, unix.SYS_MKNODAT, unix.SYS_SYMLINKAT,
	unix.SYS_LINKAT, unix.SYS_RENAMEAT, unix.SYS_RENAMEAT2,
}

// fastOpenSends are the send calls that could start a connection through
// TCP Fast Open, by syscall number and the argument holding their flags.
var fastOpenSends = []struct{ nr, flagsArg uint32 }{
	{unix.SYS_SENDTO, 3}, {unix.SYS_SENDMSG, 2}, {unix.SYS_SENDMMSG, 3},
}

// seccompArgLow is the offset of the low half of syscall argument n.
func seccompArgLow(n uint32) uint32 { return seccompDataArg0 + 8*n }

// seccompProgram mediates socket addresses and name creation, which the filter
// cannot inspect. It refuses namespace escapes, unmediated socket families and
// Fast Open. io_uring and clone3 receive ENOSYS so libraries use mediated calls.
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
	// A library may try to isolate only its thread's cwd/root/umask state.
	// Deny that exact operation without killing its process; no unshare runs.
	insts = append(insts,
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.SYS_UNSHARE, SkipFalse: 6},
		bpf.LoadAbsolute{Off: seccompDataArg0, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.CLONE_FS, SkipFalse: 3},
		bpf.LoadAbsolute{Off: seccompDataArg0 + 4, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0, SkipFalse: 1},
		bpf.RetConstant{Val: uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EPERM)},
		bpf.RetConstant{Val: kill},
	)
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
	eafnosupport := uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EAFNOSUPPORT)
	families := len(isolatedSocketFamilies)
	for _, nr := range socketCreators {
		// The domain and type are ints compared on their low halves. AF_UNIX
		// treats SOCK_RAW as SOCK_DGRAM; deny both spellings.
		block := []bpf.Instruction{
			bpf.LoadAbsolute{Off: seccompDataArg0, Size: 4},
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.AF_UNIX, SkipTrue: uint8(families + 1)},
		}
		for i, family := range isolatedSocketFamilies {
			// A match skips the remaining compares, the refusal, and the Unix
			// type check to the allow.
			block = append(block, bpf.JumpIf{Cond: bpf.JumpEqual, Val: family, SkipTrue: uint8(families - i + 5)})
		}
		block = append(block,
			bpf.RetConstant{Val: eafnosupport},
			bpf.LoadAbsolute{Off: seccompArgLow(1), Size: 4},
			bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: socketTypeMask},
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.SOCK_DGRAM, SkipTrue: 1},
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.SOCK_RAW, SkipFalse: 1},
			bpf.RetConstant{Val: eacces},
			bpf.RetConstant{Val: unix.SECCOMP_RET_ALLOW},
		)
		insts = append(insts, bpf.JumpIf{Cond: bpf.JumpEqual, Val: nr, SkipFalse: uint8(len(block))})
		insts = append(insts, block...)
	}
	notify := uint32(unix.SECCOMP_RET_USER_NOTIF)
	for _, nr := range append(append([]uint32(nil), pathCreators...), legacyPathCreators...) {
		insts = append(insts,
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: nr, SkipFalse: 1},
			bpf.RetConstant{Val: notify},
		)
	}
	for _, open := range []struct{ nr, flagsArg uint32 }{{unix.SYS_OPENAT, 2}, {legacyOpen, 1}} {
		if open.nr == 0 {
			continue
		}
		insts = append(insts,
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: open.nr, SkipFalse: 4},
			bpf.LoadAbsolute{Off: seccompArgLow(open.flagsArg), Size: 4},
			bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: unix.O_CREAT, SkipFalse: 1},
			bpf.RetConstant{Val: notify},
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
