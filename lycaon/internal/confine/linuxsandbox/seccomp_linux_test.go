//go:build linux

package linuxsandbox

import (
	"encoding/binary"
	"testing"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

func TestSeccompProgramAssembles(t *testing.T) {
	t.Parallel()
	raw, err := seccompProgram()
	if err != nil {
		t.Fatalf("seccompProgram: %v", err)
	}
	// Arch check, syscall load, one compare and one return per escape, the
	// clone3 and clone blocks, and the final allow.
	if min := 4 + 2*len(escapeSyscalls) + 7; len(raw) < min {
		t.Fatalf("program has %d instructions, want at least %d", len(raw), min)
	}
	insts, allErr := bpf.Disassemble(raw)
	if !allErr {
		t.Fatal("program did not disassemble completely")
	}
	first, ok := insts[0].(bpf.LoadAbsolute)
	if !ok || first.Off != seccompDataArch {
		t.Fatalf("program must start by loading the architecture, got %v", insts[0])
	}
	last, ok := insts[len(insts)-1].(bpf.RetConstant)
	if !ok || last.Val != unix.SECCOMP_RET_ALLOW {
		t.Fatalf("program must end by allowing, got %v", insts[len(insts)-1])
	}
	kills := 0
	for _, inst := range insts {
		if ret, ok := inst.(bpf.RetConstant); ok && ret.Val == uint32(unix.SECCOMP_RET_KILL_PROCESS) {
			kills++
		}
	}
	if kills < len(escapeSyscalls)+2 {
		t.Fatalf("program kills on %d paths, want at least %d", kills, len(escapeSyscalls)+2)
	}
}

func TestHandledAccessesGrowWithTheABI(t *testing.T) {
	t.Parallel()
	if handledAccesses(1)&unix.LANDLOCK_ACCESS_FS_REFER != 0 {
		t.Fatal("ABI 1 must not handle refer")
	}
	if handledAccesses(2)&unix.LANDLOCK_ACCESS_FS_REFER == 0 || handledAccesses(2)&unix.LANDLOCK_ACCESS_FS_TRUNCATE != 0 {
		t.Fatal("ABI 2 handles refer but not truncate")
	}
	if handledAccesses(3)&unix.LANDLOCK_ACCESS_FS_TRUNCATE == 0 {
		t.Fatal("ABI 3 handles truncate")
	}
	if handledAccesses(5)&unix.LANDLOCK_ACCESS_FS_IOCTL_DEV == 0 || handledAccesses(4)&unix.LANDLOCK_ACCESS_FS_IOCTL_DEV != 0 {
		t.Fatal("ABI 5 handles device ioctls")
	}
	rules := landlockRules(Filesystem{WriteRoots: []string{"/proj"}, WriteFiles: []string{"/etc/x"}}, 3)
	byPath := map[string]uint64{}
	for _, r := range rules {
		byPath[r.path] = r.access
	}
	if byPath["/"] != landlockReadExecute {
		t.Fatalf("/ grants %#x, want read and execute", byPath["/"])
	}
	if byPath["/proj"] != handledAccesses(3) {
		t.Fatalf("write root grants %#x, want every handled access", byPath["/proj"])
	}
	if byPath["/etc/x"]&unix.LANDLOCK_ACCESS_FS_MAKE_REG != 0 || byPath["/etc/x"]&unix.LANDLOCK_ACCESS_FS_WRITE_FILE == 0 {
		t.Fatalf("file grant %#x must carry file accesses only", byPath["/etc/x"])
	}
}

// TestSeccompProgramDecides runs the filter over seccomp_data records. The
// test VM loads words big-endian, so the records are written that way; the
// offsets are the kernel's.
func TestSeccompProgramDecides(t *testing.T) {
	t.Parallel()
	raw, err := seccompProgram()
	if err != nil {
		t.Fatalf("seccompProgram: %v", err)
	}
	insts, ok := bpf.Disassemble(raw)
	if !ok {
		t.Fatal("program did not disassemble completely")
	}
	vm, err := bpf.NewVM(insts)
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	record := func(nr uint32, arg0, arg1Low, arg1High uint32) []byte {
		b := make([]byte, 64)
		binary.BigEndian.PutUint32(b[seccompDataNr:], nr)
		binary.BigEndian.PutUint32(b[seccompDataArch:], nativeAuditArch)
		binary.BigEndian.PutUint32(b[seccompDataArg0:], arg0)
		binary.BigEndian.PutUint32(b[seccompDataArg1Low:], arg1Low)
		binary.BigEndian.PutUint32(b[seccompDataArg1Low+4:], arg1High)
		return b
	}
	kill := int(unix.SECCOMP_RET_KILL_PROCESS)
	allow := int(unix.SECCOMP_RET_ALLOW)
	enosys := int(uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.ENOSYS))
	eacces := int(uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EACCES))
	eafnosupport := int(uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EAFNOSUPPORT))
	notify := int(uint32(unix.SECCOMP_RET_USER_NOTIF))
	argsRecord := func(nr uint32, args ...uint32) []byte {
		b := make([]byte, 64)
		binary.BigEndian.PutUint32(b[seccompDataNr:], nr)
		binary.BigEndian.PutUint32(b[seccompDataArch:], nativeAuditArch)
		for i, a := range args {
			binary.BigEndian.PutUint32(b[seccompArgLow(uint32(i)):], a)
		}
		return b
	}
	for _, tc := range []struct {
		name string
		data []byte
		want int
	}{
		{"ioctl TIOCSTI", record(unix.SYS_IOCTL, 0, unix.TIOCSTI, 0), kill},
		{"ioctl TIOCLINUX", record(unix.SYS_IOCTL, 0, unix.TIOCLINUX, 0), kill},
		{"ioctl TIOCSTI with high bits set", record(unix.SYS_IOCTL, 0, unix.TIOCSTI, 0xffffffff), kill},
		{"ioctl TCGETS", record(unix.SYS_IOCTL, 0, unix.TCGETS, 0), allow},
		{"read with a TIOCSTI-valued argument", record(unix.SYS_READ, 0, unix.TIOCSTI, 0), allow},
		{"ptrace", record(unix.SYS_PTRACE, 0, 0, 0), kill},
		{"clone into a user namespace", record(unix.SYS_CLONE, unix.CLONE_NEWUSER, 0, 0), kill},
		{"plain clone", record(unix.SYS_CLONE, unix.CLONE_VM, 0, 0), allow},
		{"clone3", record(unix.SYS_CLONE3, 0, 0, 0), enosys},
		{"unix stream socket", record(unix.SYS_SOCKET, unix.AF_UNIX, unix.SOCK_STREAM, 0), allow},
		{"unix datagram socket", record(unix.SYS_SOCKET, unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0), eacces},
		{"unix datagram socket, nonblocking", record(unix.SYS_SOCKET, unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_NONBLOCK, 0), eacces},
		{"unix raw datagram alias socket", record(unix.SYS_SOCKET, unix.AF_UNIX, unix.SOCK_RAW, 0), eacces},
		{"unix raw datagram alias socket cloexec", record(unix.SYS_SOCKET, unix.AF_UNIX, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0), eacces},
		{"unix raw datagram alias socket nonblocking", record(unix.SYS_SOCKET, unix.AF_UNIX, unix.SOCK_RAW|unix.SOCK_NONBLOCK, 0), eacces},
		{"unix raw datagram alias socket cloexec nonblocking", record(unix.SYS_SOCKET, unix.AF_UNIX, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0), eacces},
		{"unix raw datagram alias socketpair", record(unix.SYS_SOCKETPAIR, unix.AF_UNIX, unix.SOCK_RAW, 0), eacces},
		{"unix raw datagram alias socketpair cloexec", record(unix.SYS_SOCKETPAIR, unix.AF_UNIX, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0), eacces},
		{"unix raw datagram alias socketpair nonblocking", record(unix.SYS_SOCKETPAIR, unix.AF_UNIX, unix.SOCK_RAW|unix.SOCK_NONBLOCK, 0), eacces},
		{"unix raw datagram alias socketpair cloexec nonblocking", record(unix.SYS_SOCKETPAIR, unix.AF_UNIX, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0), eacces},
		{"unix seqpacket socket", record(unix.SYS_SOCKET, unix.AF_UNIX, unix.SOCK_SEQPACKET, 0), allow},
		{"inet6 stream socket", record(unix.SYS_SOCKET, unix.AF_INET6, unix.SOCK_STREAM, 0), allow},
		{"netlink route socket", record(unix.SYS_SOCKET, unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0), allow},
		{"vsock stream socket", record(unix.SYS_SOCKET, unix.AF_VSOCK, unix.SOCK_STREAM, 0), eafnosupport},
		{"vsock datagram socket", record(unix.SYS_SOCKET, unix.AF_VSOCK, unix.SOCK_DGRAM, 0), eafnosupport},
		{"vsock socketpair", record(unix.SYS_SOCKETPAIR, unix.AF_VSOCK, unix.SOCK_STREAM, 0), eafnosupport},
		{"bluetooth socket", record(unix.SYS_SOCKET, unix.AF_BLUETOOTH, unix.SOCK_STREAM, 0), eafnosupport},
		{"packet socket", record(unix.SYS_SOCKET, unix.AF_PACKET, unix.SOCK_RAW, 0), eafnosupport},
		{"tipc socket", record(unix.SYS_SOCKET, unix.AF_TIPC, unix.SOCK_RDM, 0), eafnosupport},
		{"family with high bits set", record(unix.SYS_SOCKET, unix.AF_INET|0x10000, unix.SOCK_STREAM, 0), eafnosupport},
		{"inet datagram socket", record(unix.SYS_SOCKET, unix.AF_INET, unix.SOCK_DGRAM, 0), allow},
		{"connect", record(unix.SYS_CONNECT, 3, 0, 0), notify},
		{"bind", record(unix.SYS_BIND, 3, 0, 0), notify},
		{"listen", record(unix.SYS_LISTEN, 3, 0, 0), notify},
		{"sendto", argsRecord(unix.SYS_SENDTO, 0, 0, 0, 0), allow},
		{"sendto fast open", argsRecord(unix.SYS_SENDTO, 0, 0, 0, unix.MSG_FASTOPEN), eacces},
		{"sendmsg fast open", argsRecord(unix.SYS_SENDMSG, 0, 0, unix.MSG_FASTOPEN), eacces},
		{"sendmsg", argsRecord(unix.SYS_SENDMSG, 0, 0, unix.MSG_DONTWAIT), allow},
		{"sendmmsg fast open", argsRecord(unix.SYS_SENDMMSG, 0, 0, 0, unix.MSG_FASTOPEN), eacces},
		{"inet socket", record(unix.SYS_SOCKET, unix.AF_INET, unix.SOCK_STREAM, 0), allow},
		{"inet6 socket", record(unix.SYS_SOCKET, unix.AF_INET6, unix.SOCK_DGRAM, 0), allow},
		{"socketpair", record(unix.SYS_SOCKETPAIR, unix.AF_UNIX, unix.SOCK_STREAM, 0), allow},
		{"seqpacket socketpair", record(unix.SYS_SOCKETPAIR, unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0), allow},
		{"datagram socketpair", record(unix.SYS_SOCKETPAIR, unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0), eacces},
		{"io_uring_setup", record(unix.SYS_IO_URING_SETUP, 0, 0, 0), enosys},
		{"io_uring_enter", record(unix.SYS_IO_URING_ENTER, 0, 0, 0), enosys},
		{"io_uring_register", record(unix.SYS_IO_URING_REGISTER, 0, 0, 0), enosys},
	} {
		got, err := vm.Run(tc.data)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: filter returned %#x, want %#x", tc.name, got, tc.want)
		}
	}
	foreign := record(unix.SYS_READ, 0, 0, 0)
	binary.BigEndian.PutUint32(foreign[seccompDataArch:], nativeAuditArch^1)
	if got, _ := vm.Run(foreign); got != kill {
		t.Fatalf("foreign architecture: filter returned %#x, want kill", got)
	}
}
