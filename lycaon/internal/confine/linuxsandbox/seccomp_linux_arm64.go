package linuxsandbox

import "golang.org/x/sys/unix"

const (
	nativeAuditArch = unix.AUDIT_ARCH_AARCH64
	// x32SyscallBit is unused: this architecture has no secondary ABI.
	x32SyscallBit uint32 = 0
)

// legacyPathCreators is empty: this architecture has only the *at forms.
var legacyPathCreators []uint32

// legacyOpen is 0: this architecture has no open(2).
const legacyOpen uint32 = 0

// legacyPathArgs finds nothing: this architecture has no path-only spellings.
func legacyPathArgs(_ uint32, a pathArgs) (pathArgs, bool) { return a, false }
