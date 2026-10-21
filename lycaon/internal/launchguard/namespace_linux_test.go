//go:build linux

package launchguard

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFullIdentityMapRejectsRemappedOrAdditionalRanges(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"         0          0 4294967295\n", true},
		{"0 1000 1", false},
		{"1000 1000 1", false},
		{"0 0 4294967295\n1000 1000 1", false},
		{"0 0 4294967296", false},
		{"0 0 -1", false},
		{"", false},
	} {
		if got := fullIdentityMap(tc.value); got != tc.valid {
			t.Errorf("mapping %q: got %v, want %v", tc.value, got, tc.valid)
		}
	}
}

func TestBootstrapMountAndExecutionHelpersInProcess(t *testing.T) {
	if _, err := CaptureCaller(); err == nil {
		t.Fatal("CaptureCaller succeeded without setuid transition")
	}
	if _, err := EntryAttributes(Identity{UID: 1000, GID: 1000}); err == nil {
		t.Fatal("EntryAttributes succeeded without root")
	}
	if err := WritePolicy(GenerationPolicy{Version: 1}); err == nil {
		t.Fatal("WritePolicy succeeded without root")
	}

	cwd, err := os.Getwd()
	testutil.FailErr(t, "getwd", err)
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
		_ = unix.Prctl(unix.PR_SET_DUMPABLE, 1, 0, 0, 0)
	})
	testutil.FailErr(t, "sanitizeProcess", sanitizeProcess())

	base := filepath.Join(t.TempDir(), "anchor")
	testutil.FailErr(t, "mkdir base", os.MkdirAll(base, 0o755))
	testutil.FailErr(t, "chmod base", os.Chmod(base, 0o755))
	withTestTrustAnchor(t, base)

	self, err := openSelf()
	if err == nil {
		_ = self.Close()
	}

	b1, b2, err := brokerPair()
	testutil.FailErr(t, "brokerPair", err)
	_ = b1.Close()
	_ = b2.Close()

	f1Path := filepath.Join(base, "f1")
	f2Path := filepath.Join(base, "f2")
	testutil.FailErr(t, "write f1", os.WriteFile(f1Path, []byte("1"), 0o755))
	testutil.FailErr(t, "chmod f1", os.Chmod(f1Path, 0o755))
	testutil.FailErr(t, "write f2", os.WriteFile(f2Path, []byte("2"), 0o644))
	testutil.FailErr(t, "chmod f2", os.Chmod(f2Path, 0o644))
	f1, err := os.Open(f1Path)
	testutil.FailErr(t, "open f1", err)
	defer func() { _ = f1.Close() }()
	f2, err := os.Open(f2Path)
	testutil.FailErr(t, "open f2", err)
	defer func() { _ = f2.Close() }()

	testutil.FailErr(t, "sameObject equal", sameObject(f1, f1))
	if err := sameObject(f1, f2); err == nil {
		t.Fatal("sameObject accepted distinct files")
	}
	testutil.FailErr(t, "rootRegular f1", rootRegular(f1))
	if err := verifyNamespace(f1); err == nil {
		t.Fatal("verifyNamespace accepted regular file")
	}
	if err := freshMountNamespace(); err == nil {
		t.Fatal("freshMountNamespace accepted host mount namespace")
	}
	if !DropPrivilegesForError() {
		t.Fatal("DropPrivilegesForError returned false for unprivileged test")
	}
	_ = dropPrivileges(Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), Groups: []uint32{uint32(os.Getgid())}})

	// bindGeneration validation paths before mount operations.
	if _, err := bindGeneration(GenerationPolicy{}, f1, f1); err == nil {
		t.Fatal("bindGeneration accepted non-directory generation")
	}
	dir1 := filepath.Join(base, "gen1")
	dir2 := filepath.Join(base, "gen2")
	installDir := filepath.Join(base, "install")
	for _, d := range []string{dir1, dir2, installDir} {
		testutil.FailErr(t, "mkdir "+d, os.MkdirAll(d, 0o755))
		testutil.FailErr(t, "chmod "+d, os.Chmod(d, 0o755))
	}
	dir1File, err := os.Open(dir1)
	testutil.FailErr(t, "open dir1", err)
	defer func() { _ = dir1File.Close() }()
	if _, err := bindGeneration(GenerationPolicy{LogicalInstallRoot: filepath.Join(base, "missing"), GenerationRoot: dir1}, dir1File, f1); err == nil {
		t.Fatal("bindGeneration accepted missing LogicalInstallRoot")
	}
	if _, err := bindGeneration(GenerationPolicy{LogicalInstallRoot: installDir, GenerationRoot: filepath.Join(base, "missing")}, dir1File, f1); err == nil {
		t.Fatal("bindGeneration accepted missing GenerationRoot")
	}
	if _, err := bindGeneration(GenerationPolicy{LogicalInstallRoot: installDir, GenerationRoot: dir2}, dir1File, f1); err == nil {
		t.Fatal("bindGeneration accepted mismatched GenerationRoot")
	}
	if _, err := bindGeneration(GenerationPolicy{LogicalInstallRoot: installDir, GenerationRoot: dir1}, dir1File, f1); err == nil {
		t.Fatal("bindGeneration accepted host mount namespace")
	}

	if _, err := programInterpreter(f1); err == nil {
		t.Fatal("programInterpreter accepted non-ELF file")
	}
	if err := restrictExecution(GenerationPolicy{LogicalInstallRoot: base}, f1); err == nil {
		t.Fatal("restrictExecution accepted non-ELF file")
	}
	if sh, err := os.Open("/bin/sh"); err == nil {
		defer func() { _ = sh.Close() }()
		_, _ = programInterpreter(sh)
	}
	if exe, err := os.Open("/proc/self/exe"); err == nil {
		defer func() { _ = exe.Close() }()
		_, _ = programInterpreter(exe)
		if err := restrictExecution(GenerationPolicy{LogicalInstallRoot: base, SystemExecutables: []string{f1Path, filepath.Join(base, "missing")}}, exe); err == nil {
			t.Fatal("restrictExecution accepted missing system executable")
		}
		done := make(chan error, 1)
		go func() {
			runtime.LockOSThread()
			done <- restrictExecution(GenerationPolicy{LogicalInstallRoot: base, SystemExecutables: []string{f1Path}}, exe)
		}()
		_ = <-done
	}

	attr := unix.LandlockRulesetAttr{Handled_access_fs: unix.LANDLOCK_ACCESS_FS_EXECUTE}
	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Offsetof(attr.Handled_access_net), 0)
	if errno == 0 {
		defer func() { _ = unix.Close(int(ruleset)) }()
		testutil.FailErr(t, "allowExecution dir", allowExecution(int(ruleset), base))
		testutil.FailErr(t, "allowExecution file", allowExecution(int(ruleset), f1Path))
	}
}
