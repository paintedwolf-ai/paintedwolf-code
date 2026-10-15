package linuxsandbox

import "golang.org/x/sys/unix"

const (
	nativeAuditArch = unix.AUDIT_ARCH_X86_64
	// x32SyscallBit marks the x32 ABI's syscall numbers, which share this
	// architecture's filter and are refused.
	x32SyscallBit uint32 = 0x40000000
)
