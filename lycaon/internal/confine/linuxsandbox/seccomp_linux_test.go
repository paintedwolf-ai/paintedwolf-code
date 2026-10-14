//go:build linux

package linuxsandbox

import (
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
