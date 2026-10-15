package linuxsandbox

import "golang.org/x/sys/unix"

const (
	nativeAuditArch = unix.AUDIT_ARCH_AARCH64
	// x32SyscallBit is unused: this architecture has no secondary ABI.
	x32SyscallBit uint32 = 0
)
