package linuxsandbox

import "golang.org/x/sys/unix"

const (
	nativeAuditArch = unix.AUDIT_ARCH_X86_64
	// x32SyscallBit marks the x32 ABI's syscall numbers, which share this
	// architecture's filter and are refused.
	x32SyscallBit uint32 = 0x40000000
)

// legacyPathCreators are this architecture's path-only spellings of the calls
// that create, link, or move a name; arm64 has only the *at forms.
var legacyPathCreators = []uint32{
	unix.SYS_MKDIR, unix.SYS_MKNOD, unix.SYS_SYMLINK, unix.SYS_LINK, unix.SYS_RENAME, unix.SYS_CREAT,
}

// legacyOpen is open(2), whose flags are its second argument; 0 where absent.
const legacyOpen uint32 = unix.SYS_OPEN

// legacyPathArgs places the arguments of the path-only spellings, which name
// paths from the working directory.
func legacyPathArgs(nr uint32, a pathArgs) (pathArgs, bool) {
	switch nr {
	case unix.SYS_OPEN:
		a.op, a.path, a.flags, a.mode = callOpen, 0, 1, 2
	case unix.SYS_CREAT:
		a.op, a.path, a.mode = callOpen, 0, 1
		a.fixedFlags = unix.O_CREAT | unix.O_WRONLY | unix.O_TRUNC
	case unix.SYS_MKDIR:
		a.op, a.path, a.mode = callMkdir, 0, 1
	case unix.SYS_MKNOD:
		a.op, a.path, a.mode, a.dev = callMknod, 0, 1, 2
	case unix.SYS_SYMLINK:
		a.op, a.content, a.path = callSymlink, 0, 1
	case unix.SYS_LINK:
		a.op, a.path, a.newPath = callLink, 0, 1
	case unix.SYS_RENAME:
		a.op, a.path, a.newPath = callRename, 0, 1
	default:
		return a, false
	}
	return a, true
}
