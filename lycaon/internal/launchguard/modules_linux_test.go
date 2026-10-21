//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"

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
	if _, err := enterModuleView(policy, file); err == nil {
		t.Fatal("enterModuleView accepted a non-ELF image")
	}
	if err := allowModules(t.TempDir()); err == nil {
		t.Fatal("allowModules accepted a caller-owned directory")
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
}

func verifyModuleViewIsolation(dir, rawPID string) error {
	runtime.LockOSThread()
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return err
	}
	if err := checkLiveDesktopMountIsolation(dir, rawPID); err != nil {
		return err
	}
	return checkDirectModuleViewIsolation(dir)
}

func checkLiveDesktopMountIsolation(dir, rawPID string) error {
	pid, err := strconv.Atoi(rawPID)
	if err != nil || pid <= 1 {
		return fmt.Errorf("invalid desktop pid %q", rawPID)
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
	checkErr := assertIsolatedNoexecView(lateFile, underlying)
	if restoreErr := unix.Setns(int(hostNS.Fd()), unix.CLONE_NEWNS); restoreErr != nil && checkErr == nil {
		checkErr = restoreErr
	}
	return checkErr
}

func checkDirectModuleViewIsolation(dir string) error {
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return err
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return err
	}
	lateDir, underlying, lateFile, err := prepareLateMountPoint(filepath.Join(dir, "direct-late"))
	if err != nil {
		return err
	}
	if err := unix.Mount(dir, dir, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return err
	}
	if err := unix.Mount("", dir, "", unix.MS_SHARED, ""); err != nil {
		return err
	}
	parentNS, err := os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		return err
	}
	defer func() { _ = parentNS.Close() }()
	viewNS, stageNS, err := buildStageAndModuleViews(dir)
	if err != nil {
		return err
	}
	defer func() {
		_ = viewNS.Close()
		_ = stageNS.Close()
	}()
	if err := unix.Setns(int(parentNS.Fd()), unix.CLONE_NEWNS); err != nil {
		return err
	}
	if err := mountExecutableTmpfs(lateDir, lateFile); err != nil {
		return err
	}
	if err := unix.Setns(int(stageNS.Fd()), unix.CLONE_NEWNS); err != nil {
		return err
	}
	if err := canMapExecutable(lateFile); err != nil {
		return fmt.Errorf("broker view did not receive host mount: %w", err)
	}
	if err := unix.Setns(int(viewNS.Fd()), unix.CLONE_NEWNS); err != nil {
		return err
	}
	return assertIsolatedNoexecView(lateFile, underlying)
}

func buildStageAndModuleViews(dir string) (viewNS, stageNS *os.File, err error) {
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return nil, nil, err
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_SLAVE, ""); err != nil {
		return nil, nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "launch.json"))
	if err != nil {
		return nil, nil, err
	}
	policy, err := decodePolicy(raw)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.Mount(policy.GenerationRoot, policy.LogicalInstallRoot, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return nil, nil, err
	}
	desktop, err := os.Open(filepath.Join(policy.LogicalInstallRoot, "bin/painted-wolf-code"))
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = desktop.Close() }()
	leave, err := enterModuleView(policy, desktop)
	if err != nil {
		return nil, nil, err
	}
	viewed, err := reopenImage(policy, desktop)
	if err != nil {
		_ = leave()
		return nil, nil, err
	}
	_ = viewed.Close()
	viewNS, err = os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		_ = leave()
		return nil, nil, err
	}
	if err := leave(); err != nil {
		_ = viewNS.Close()
		return nil, nil, err
	}
	stageNS, err = os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		_ = viewNS.Close()
		return nil, nil, err
	}
	return viewNS, stageNS, nil
}

func prepareLateMountPoint(lateDir string) (dir, underlying, lateFile string, err error) {
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

func assertIsolatedNoexecView(lateFile, underlying string) error {
	if _, err := os.Stat(lateFile); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("post-launch mount propagated into module view: stat %s = %v", lateFile, err)
	}
	if err := canMapExecutable(underlying); !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("underlying mount in module view mapped executable: %v, want EPERM", err)
	}
	return nil
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
