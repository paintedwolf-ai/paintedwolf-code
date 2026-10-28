//go:build linux

package launchguard

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
	if _, err := enterModuleView(policy, file); err == nil {
		t.Fatal("enterModuleView accepted a non-ELF image")
	}
	if err := allowModules(t.TempDir()); err == nil {
		t.Fatal("allowModules accepted a caller-owned directory")
	}
	if _, err := reopenImage(GenerationPolicy{LogicalInstallRoot: t.TempDir()}, file); err == nil {
		t.Fatal("reopenImage accepted a missing desktop image")
	}
	if _, err := allowedModuleMountPoints(GenerationPolicy{LogicalInstallRoot: t.TempDir()}); err == nil {
		t.Fatal("allowedModuleMountPoints accepted a missing desktop image")
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
	if _, err := allowedModuleMountPoints(GenerationPolicy{LogicalInstallRoot: otherRoot}); err == nil {
		t.Fatal("allowedModuleMountPoints accepted a non-ELF desktop image")
	}
	checkMountInfoHelpers(t)
}

func checkMountInfoHelpers(t *testing.T) {
	t.Helper()
	if got := unescapeMountPath(`/media/pw/USB\040Drive\011Tab\012NL\134Back\400\999`); got != "/media/pw/USB Drive\tTab\nNL\\Back\\400\\999" {
		t.Fatalf("unescapeMountPath = %q", got)
	}
	raw := []byte("20 1 8:1 / / rw,noexec,relatime - ext4 /dev/sda1 rw\n" +
		"21 20 0:25 / /opt/paintedwolf ro,nosuid,nodev - ext4 /dev/sda1 ro\n" +
		"22 20 0:26 / /usr/lib rw,relatime - ext4 /dev/sda1 rw\n" +
		"23 20 0:27 / /media/pw/USB\\040Drive\\011Tab\\012NL\\134Back\rCR rw,relatime shared:1 - vfat /dev/sdb1 rw\n" +
		"24 23 0:28 / /media/pw/USB\\040Drive\\011Tab\\012NL\\134Back\rCR rw,relatime - tmpfs tmpfs rw\n" +
		"25 20 0:29 / /mnt/done rw,noexec,nosuid,nodev - tmpfs tmpfs rw\n" +
		"26 20 0:30 / /mnt/nul\\000bad rw - tmpfs tmpfs rw\n" +
		"27 20 0:31 / relative/mnt rw - tmpfs tmpfs rw\n" +
		"28 20 0:32 / /mnt/no-sep rw tmpfs tmpfs rw\n" +
		"bad line\n" +
		"x y 0:0 / /mnt rw - tmpfs tmpfs rw\n")
	entries := parseMountInfo(raw)
	if len(entries) != 6 || entries[3].mountPoint != "/media/pw/USB Drive\tTab\nNL\\Back\rCR" {
		t.Fatalf("parseMountInfo = %+v", entries)
	}
	if !isShadowedMount(entries[3], entries) || isShadowedMount(entries[4], entries) {
		t.Fatalf("shadowed mount detection mismatch: %+v", entries)
	}
	exempt := baselineExemptMounts(entries[:3], "/opt/paintedwolf", []string{"/usr/lib"})
	if !exempt[20] || !exempt[21] || !exempt[22] || exempt[23] {
		t.Fatalf("baselineExemptMounts = %v", exempt)
	}
	failed := map[int]string{25: "/mnt/done\x00rw"}
	scrubModuleMounts(entries, exempt, failed)
	if _, ok := failed[25]; ok {
		t.Fatalf("completed mount kept failure record: %v", failed)
	}
	if failed[24] == "" {
		t.Fatalf("unprivileged scrub of missing mount point did not record failure: %v", failed)
	}
	scrubModuleMounts(entries, exempt, failed)
	checkScrubSingleMountRejectsSymlinksAndMagiclinks(t)
}

func verifyModuleViewIsolation(dir, rawPID string) error {
	runtime.LockOSThread()
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return err
	}
	if err := checkLiveDesktopMountIsolation(dir, rawPID); err != nil {
		return err
	}
	if err := checkDirectModuleViewIsolation(dir); err != nil {
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
	checkErr := assertScrubbedNoexecView(lateDir, lateFile, underlying, existing)
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

func checkDirectModuleViewIsolation(dir string) error {
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return err
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return err
	}
	existing := filepath.Join(dir, "direct-existing.so")
	if err := os.WriteFile(existing, []byte("existing"), 0o755); err != nil {
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
	viewNS, stageNS, stopWatcher, err := buildStageAndModuleViews(dir)
	if err != nil {
		return err
	}
	defer func() {
		stopWatcher()
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
	return assertScrubbedNoexecView(lateDir, lateFile, underlying, existing)
}

func buildStageAndModuleViews(dir string) (viewNS, stageNS *os.File, stop func(), err error) {
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return nil, nil, nil, err
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_SLAVE, ""); err != nil {
		return nil, nil, nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "launch.json"))
	if err != nil {
		return nil, nil, nil, err
	}
	policy, err := decodePolicy(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := unix.Mount(policy.GenerationRoot, policy.LogicalInstallRoot, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return nil, nil, nil, err
	}
	desktop, err := os.Open(filepath.Join(policy.LogicalInstallRoot, "bin/painted-wolf-code"))
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = desktop.Close() }()
	leave, err := enterModuleView(policy, desktop)
	if err != nil {
		return nil, nil, nil, err
	}
	viewed, err := reopenImage(policy, desktop)
	if err != nil {
		_ = leave()
		return nil, nil, nil, err
	}
	_ = viewed.Close()
	viewNS, err = os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		_ = leave()
		return nil, nil, nil, err
	}
	stop, err = startInProcessModuleWatcher(policy, viewNS)
	if err != nil {
		_ = viewNS.Close()
		_ = leave()
		return nil, nil, nil, err
	}
	if err := leave(); err != nil {
		stop()
		_ = viewNS.Close()
		return nil, nil, nil, err
	}
	stageNS, err = os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		stop()
		_ = viewNS.Close()
		return nil, nil, nil, err
	}
	return viewNS, stageNS, stop, nil
}

func startInProcessModuleWatcher(policy GenerationPolicy, viewNS *os.File) (func(), error) {
	roots, err := allowedModuleMountPoints(policy)
	if err != nil {
		return nil, err
	}
	watchR, watchW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		_ = unix.Unshare(unix.CLONE_FS)
		_ = unix.Setns(int(viewNS.Fd()), unix.CLONE_NEWNS)
		mountinfo, err := os.Open("/proc/thread-self/mountinfo")
		if err != nil {
			_ = readyW.Close()
			done <- err
			return
		}
		defer func() { _ = mountinfo.Close() }()
		done <- watchModuleMounts(int(mountinfo.Fd()), int(watchR.Fd()), int(readyW.Fd()), policy.LogicalInstallRoot, roots)
	}()
	var ack [1]byte
	_, err = io.ReadFull(readyR, ack[:])
	_ = readyR.Close()
	if err != nil {
		_ = watchW.Close()
		_ = watchR.Close()
		return nil, err
	}
	return func() { _ = watchW.Close(); <-done; _ = watchR.Close() }, nil
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

func assertScrubbedNoexecView(lateDir, lateFile, underlying, existing string) error {
	if err := canMapExecutable(existing); !errors.Is(err, unix.EPERM) {
		if err == nil {
			return errors.New("existing mount in module view mapped executable, want EPERM")
		}
		return fmt.Errorf("existing mount in module view mapped executable: %w, want EPERM", err)
	}
	if _, err := os.Stat(underlying); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return fmt.Errorf("post-launch mount did not cover underlying directory in module view: %s is still visible", underlying)
		}
		return fmt.Errorf("post-launch mount did not cover underlying directory in module view: stat %s: %w", underlying, err)
	}
	content, err := os.ReadFile(lateFile)
	if err != nil {
		return fmt.Errorf("post-launch mount not readable in module view: %w", err)
	}
	if !bytes.Equal(content, []byte("late-module")) {
		return fmt.Errorf("post-launch mount content in module view: %q, want late-module", content)
	}
	const bound = 100 * time.Millisecond
	deadline := time.Now().Add(bound)
	for {
		mapErr := canMapExecutable(lateFile)
		var st unix.Statfs_t
		statErr := unix.Statfs(lateDir, &st)
		const wantFlags = unix.ST_NOEXEC | unix.ST_NOSUID | unix.ST_NODEV
		if errors.Is(mapErr, unix.EPERM) && statErr == nil && st.Flags&wantFlags == wantFlags {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("post-launch mount was not scrubbed within %s: map=%v statErr=%v flags=%#x", bound, mapErr, statErr, st.Flags)
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
