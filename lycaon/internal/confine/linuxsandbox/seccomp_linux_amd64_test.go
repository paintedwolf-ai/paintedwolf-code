package linuxsandbox

import (
	"encoding/binary"
	"testing"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// The path-only spellings reach the supervisor like their *at forms.
func TestSeccompProgramNotifiesLegacyPathCreators(t *testing.T) {
	t.Parallel()
	raw, err := seccompProgram()
	if err != nil {
		t.Fatalf("seccompProgram: %v", err)
	}
	insts, _ := bpf.Disassemble(raw)
	vm, err := bpf.NewVM(insts)
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	run := func(nr uint32, args ...uint32) int {
		b := make([]byte, 64)
		binary.BigEndian.PutUint32(b[seccompDataNr:], nr)
		binary.BigEndian.PutUint32(b[seccompDataArch:], nativeAuditArch)
		for i, a := range args {
			binary.BigEndian.PutUint32(b[seccompArgLow(uint32(i)):], a)
		}
		got, err := vm.Run(b)
		if err != nil {
			t.Fatalf("run %d: %v", nr, err)
		}
		return got
	}
	notify := int(uint32(unix.SECCOMP_RET_USER_NOTIF))
	for _, nr := range []uint32{unix.SYS_MKDIR, unix.SYS_MKNOD, unix.SYS_SYMLINK, unix.SYS_LINK, unix.SYS_RENAME, unix.SYS_CREAT} {
		if got := run(nr); got != notify {
			t.Errorf("syscall %d: %#x, want notify", nr, got)
		}
	}
	if got := run(unix.SYS_OPEN, 0, unix.O_CREAT|unix.O_WRONLY); got != notify {
		t.Errorf("open with O_CREAT: %#x", got)
	}
	if got := run(unix.SYS_OPEN, 0, unix.O_RDONLY); got != int(unix.SECCOMP_RET_ALLOW) {
		t.Errorf("open without O_CREAT: %#x", got)
	}
	for nr := range map[uint32]bool{unix.SYS_OPEN: true, unix.SYS_CREAT: true, unix.SYS_MKDIR: true, unix.SYS_RENAME: true, unix.SYS_LINK: true, unix.SYS_SYMLINK: true, unix.SYS_MKNOD: true} {
		if _, ok := pathArgsOf(nr); !ok || !isPathCall(int32(nr)) {
			t.Errorf("syscall %d has no argument layout", nr)
		}
	}
	if a, _ := pathArgsOf(unix.SYS_CREAT); a.fixedFlags != unix.O_CREAT|unix.O_WRONLY|unix.O_TRUNC {
		t.Errorf("creat flags %#x", a.fixedFlags)
	}
}
