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
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
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
		mountinfo, err := unix.Open("/proc/thread-self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			_ = readyW.Close()
			done <- err
			return
		}
		defer func() { _ = unix.Close(mountinfo) }()
		done <- watchModuleMounts(mountinfo, int(watchR.Fd()), int(readyW.Fd()), policy.LogicalInstallRoot, roots)
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
			if mapErr == nil {
				mapErr = errors.New("late module still maps executable")
			}
			return fmt.Errorf("post-launch mount was not scrubbed within %s (flags=%#x): %w", bound, st.Flags, errors.Join(mapErr, statErr))
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

func TestReopenImageReturnsTheRetainedSelectionInode(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "bin/painted-wolf-code")
	if err := os.MkdirAll(filepath.Dir(image), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(image, []byte("desktop"), 0o755); err != nil {
		t.Fatalf("write desktop image: %v", err)
	}
	selected, err := os.Open(image)
	if err != nil {
		t.Fatalf("open selected image: %v", err)
	}
	defer func() { _ = selected.Close() }()
	reopened, err := reopenImage(GenerationPolicy{LogicalInstallRoot: root}, selected)
	if err != nil {
		t.Fatalf("reopenImage: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	var want, got unix.Stat_t
	if err := unix.Fstat(int(selected.Fd()), &want); err != nil {
		t.Fatalf("fstat selected image: %v", err)
	}
	if err := unix.Fstat(int(reopened.Fd()), &got); err != nil {
		t.Fatalf("fstat reopened image: %v", err)
	}
	if got.Dev != want.Dev || got.Ino != want.Ino {
		t.Fatalf("reopened image = %d:%d, want the selected inode %d:%d", got.Dev, got.Ino, want.Dev, want.Ino)
	}
}

func TestAllowedModuleMountPointsFollowTheDesktopImageInterpreter(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "bin/painted-wolf-code")
	if err := os.MkdirAll(filepath.Dir(image), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	copyTestExecutable(t, image)
	desktop, err := os.Open(image)
	if err != nil {
		t.Fatalf("open desktop image: %v", err)
	}
	defer func() { _ = desktop.Close() }()
	interpreter, err := programInterpreter(desktop)
	if err != nil {
		t.Fatalf("programInterpreter: %v", err)
	}
	policy := GenerationPolicy{
		LogicalInstallRoot: root,
		LibraryRoots:       []string{"/usr/lib", "/usr/lib64"},
		SystemExecutables:  []string{"/usr/libexec/webkit2gtk-4.1"},
	}
	got, err := allowedModuleMountPoints(policy)
	if err != nil {
		t.Fatalf("allowedModuleMountPoints: %v", err)
	}
	if want := moduleRoots(policy, interpreter); !slices.Equal(got, want) {
		t.Fatalf("allowedModuleMountPoints = %v, want %v", got, want)
	}
}

func copyTestExecutable(t *testing.T, destination string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("test executable: %v", err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatalf("open test executable: %v", err)
	}
	defer func() { _ = source.Close() }()
	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatalf("create %s: %v", destination, err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		t.Fatalf("copy test executable: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("close %s: %v", destination, err)
	}
}

// The watcher arms its baseline from the mount table it is given, reports
// readiness once, and ends when the last writer of its lifetime pipe closes.
// With the logical installation root at "/" every mount is exempt, so the
// test's own mount namespace is left untouched.
func TestWatchModuleMountsArmsItsBaselineAndEndsWithItsLifetime(t *testing.T) {
	mountinfo, err := unix.Open("/proc/thread-self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open mountinfo: %v", err)
	}
	defer func() { _ = unix.Close(mountinfo) }()
	var lifetime, ready [2]int
	if err := unix.Pipe2(lifetime[:], unix.O_CLOEXEC); err != nil {
		t.Fatalf("lifetime pipe: %v", err)
	}
	defer func() { _ = unix.Close(lifetime[0]) }()
	// The watcher closes the ready write end itself once its baseline is armed.
	if err := unix.Pipe2(ready[:], unix.O_CLOEXEC); err != nil {
		_ = unix.Close(lifetime[1])
		t.Fatalf("ready pipe: %v", err)
	}
	defer func() { _ = unix.Close(ready[0]) }()
	done := make(chan error, 1)
	go func() {
		done <- watchModuleMounts(mountinfo, lifetime[0], ready[1], "/", nil)
	}()
	var armed [1]byte
	if n, err := unix.Read(ready[0], armed[:]); err != nil || n != 1 || armed[0] != 1 {
		_ = unix.Close(lifetime[1])
		t.Fatalf("watcher readiness = %v (%d bytes, %v), want the single byte 1", armed, n, err)
	}
	if n, err := unix.Read(ready[0], armed[:]); err != nil || n != 0 {
		_ = unix.Close(lifetime[1])
		t.Fatalf("ready pipe after arming = %d bytes, %v; want end of file", n, err)
	}
	if err := unix.Close(lifetime[1]); err != nil {
		t.Fatalf("close lifetime writer: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("watchModuleMounts after its lifetime ended: %v", err)
	}
}

func checkInstrumentedModulePaths(t *testing.T, dir string) error {
	runtime.LockOSThread()
	testutil.FailErr(t, "private actor filesystem", unix.Unshare(unix.CLONE_FS|unix.CLONE_NEWNS))
	testutil.FailErr(t, "private actor mounts", unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""))
	testutil.FailErr(t, "actor subreaper", unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0))
	t.Cleanup(func() {
		tasks, err := os.ReadDir("/proc/self/task")
		testutil.FailErr(t, "fixture child threads", err)
		for _, task := range tasks {
			raw, _ := os.ReadFile(filepath.Join("/proc/self/task", task.Name(), "children"))
			for _, child := range strings.Fields(string(raw)) {
				pid, err := strconv.Atoi(child)
				testutil.FailErr(t, "fixture child identity", err)
				_ = unix.Kill(pid, unix.SIGKILL)
			}
		}
		for {
			var status unix.WaitStatus
			_, err := unix.Wait4(-1, &status, 0, nil)
			if errors.Is(err, unix.ECHILD) {
				break
			}
			if !errors.Is(err, unix.EINTR) {
				testutil.FailErr(t, "reap fixture child", err)
			}
		}
	})
	spec, frame := moduleFixtureSelection(t, dir)
	testutil.FailErr(t, "fixture generation mount", unix.Mount(spec.Generation.GenerationRoot, spec.Generation.LogicalInstallRoot, "", unix.MS_BIND|unix.MS_REC, ""))
	checkInstrumentedModuleStage(t, dir, spec, frame)
	checkInstrumentedBrokerAgent(t, spec, frame)
	checkInstrumentedModuleWatcher(t, dir, spec, frame)
	return nil
}
