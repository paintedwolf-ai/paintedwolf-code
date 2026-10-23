//go:build linux

package launchguard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const fixtureDesktopPIDEnv = "PW_LAUNCH_DESKTOP_PID"

func TestModuleRootsIncludesLibrariesSystemExecutablesAndInterpreter(t *testing.T) {
	policy := GenerationPolicy{
		LibraryRoots:      []string{"/usr/lib", "/usr/lib64"},
		SystemExecutables: []string{"/usr/libexec/webkit2gtk-4.1"},
	}
	got := moduleRoots(policy, "/lib64/ld-linux-x86-64.so.2")
	want := []string{"/usr/lib", "/usr/lib64", "/usr/libexec/webkit2gtk-4.1", "/lib64/ld-linux-x86-64.so.2"}
	if !slices.Equal(got, want) {
		t.Fatalf("moduleRoots = %v, want %v", got, want)
	}
	withoutInterp := moduleRoots(policy, "")
	if !slices.Equal(withoutInterp, want[:3]) {
		t.Fatalf("moduleRoots without interpreter = %v, want %v", withoutInterp, want[:3])
	}
	badImage := filepath.Join(t.TempDir(), "not-elf")
	if err := os.WriteFile(badImage, []byte("not-an-elf"), 0o755); err != nil {
		t.Fatalf("write invalid image: %v", err)
	}
	file, err := os.Open(badImage)
	if err != nil {
		t.Fatalf("open invalid image: %v", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := captureModuleCodeRoots(policy, file); err == nil {
		t.Fatal("captureModuleCodeRoots accepted a non-ELF image")
	}
	if _, err := reopenImage(GenerationPolicy{LogicalInstallRoot: t.TempDir()}, file); err == nil {
		t.Fatal("reopenImage accepted a missing desktop image")
	}
	otherRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(otherRoot, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(otherRoot, "bin/painted-wolf-code"), []byte("other"), 0o755); err != nil {
		t.Fatalf("write other desktop image: %v", err)
	}
	if _, err := reopenImage(GenerationPolicy{LogicalInstallRoot: otherRoot}, file); err == nil {
		t.Fatal("reopenImage accepted a different desktop inode")
	}

	checkMountInfoHelpers(t)
}

func checkMountInfoHelpers(t *testing.T) {
	t.Helper()
	if got := unescapeMountPath(`/media/pw/USB\040Drive\011Tab\012NL\134Back\400\999`); got != "/media/pw/USB Drive\tTab\nNL\\Back\\400\\999" {
		t.Fatalf("unescapeMountPath = %q", got)
	}
	checkMountTargetRejectsSymlinksAndMagiclinks(t)
}

func verifyModuleViewIsolation(dir, rawPID string) error {
	runtime.LockOSThread()
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return err
	}
	if err := checkLiveDesktopMountIsolation(dir, rawPID); err != nil {
		return err
	}
	return checkModuleWatcherSeal()
}

func checkLiveDesktopMountIsolation(dir, rawPID string) error {
	pid, err := strconv.Atoi(rawPID)
	if err != nil || pid <= 1 {
		return fmt.Errorf("invalid desktop pid %q", rawPID)
	}
	if err := verifyLiveWatcherProcess(pid); err != nil {
		return err
	}
	existing := filepath.Join(dir, "live-existing.so")
	if err := os.WriteFile(existing, []byte("existing"), 0o755); err != nil {
		return err
	}
	lateDir, underlying, lateFile, err := prepareLateMountPoint(filepath.Join(dir, "live-late"))
	if err != nil {
		return err
	}
	if err := mountExecutableTmpfs(lateDir, lateFile); err != nil {
		return err
	}
	defer func() { _ = unix.Unmount(lateDir, unix.MNT_DETACH) }()
	if err := canMapExecutable(lateFile); err != nil {
		return fmt.Errorf("host mount is not executable: %w", err)
	}
	if err := checkChildUserNamespaceMountSetattrRefused(lateDir); err != nil {
		return err
	}
	hostNS, err := os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		return err
	}
	defer func() { _ = hostNS.Close() }()
	desktopNS, err := os.Open(fmt.Sprintf("/proc/%d/ns/mnt", pid))
	if err != nil {
		return err
	}
	defer func() { _ = desktopNS.Close() }()
	if err := unix.Setns(int(desktopNS.Fd()), unix.CLONE_NEWNS); err != nil {
		return fmt.Errorf("enter live desktop mount namespace: %w", err)
	}
	checkErr := assertMediatedNoexecView(lateDir, lateFile, underlying, existing)
	if restoreErr := unix.Setns(int(hostNS.Fd()), unix.CLONE_NEWNS); restoreErr != nil && checkErr == nil {
		checkErr = restoreErr
	}
	if checkErr != nil {
		return checkErr
	}
	if err := canMapExecutable(lateFile); err != nil {
		return fmt.Errorf("host mount lost executable mapping outside module view: %w", err)
	}
	return nil
}

func prepareLateMountPoint(baseDir string) (dir, underlying, lateFile string, err error) {
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return "", "", "", err
	}
	lateDir := filepath.Join(baseDir, "USB Drive\tTab\nLine\rCR\\Back")
	if err := os.MkdirAll(lateDir, 0o755); err != nil {
		return "", "", "", err
	}
	underlying = filepath.Join(lateDir, "underlying.so")
	if err := os.WriteFile(underlying, []byte("underlying"), 0o755); err != nil {
		return "", "", "", err
	}
	return lateDir, underlying, filepath.Join(lateDir, "late.so"), nil
}

func mountExecutableTmpfs(lateDir, lateFile string) error {
	if err := unix.Mount("tmpfs", lateDir, "tmpfs", 0, "mode=0755"); err != nil {
		return err
	}
	return os.WriteFile(lateFile, []byte("late-module"), 0o755)
}

func assertMediatedNoexecView(lateDir, lateFile, underlying, existing string) error {
	if err := canMapExecutable(existing); !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("existing module mapped executable: %v", err)
	}
	const bound = 100 * time.Millisecond
	deadline := time.Now().Add(bound)
	for {
		content, err := os.ReadFile(lateFile)
		if err == nil {
			if !bytes.Equal(content, []byte("late-module")) {
				return errors.New("attached volume content differs from its source")
			}
			if _, err := os.Stat(underlying); !errors.Is(err, os.ErrNotExist) {
				return errors.New("attached volume did not cover the underlying directory")
			}
			mapErr := canMapExecutable(lateFile)
			var stat unix.Statfs_t
			statErr := unix.Statfs(lateDir, &stat)
			const flags = unix.ST_NOEXEC | unix.ST_NOSUID | unix.ST_NODEV
			if !errors.Is(mapErr, unix.EPERM) || statErr != nil || stat.Flags&flags != flags {
				return fmt.Errorf("visible volume was not already safe: map=%v stat=%v flags=%#x", mapErr, statErr, stat.Flags)
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("safe volume was not attached within %s", bound)
		}
		time.Sleep(50 * time.Microsecond)
	}
}

func canMapExecutable(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	region, err := unix.Mmap(int(file.Fd()), 0, 1, unix.PROT_READ|unix.PROT_EXEC, unix.MAP_PRIVATE)
	if err != nil {
		return err
	}
	return unix.Munmap(region)
}
