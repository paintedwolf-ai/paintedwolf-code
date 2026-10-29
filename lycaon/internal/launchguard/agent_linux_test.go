//go:build linux

package launchguard

import (
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchbroker"
	"github.com/lycaon/lycaon/internal/testutil"
)

const wantWatcherCapHex = "0000000000200004"
const wantZeroCapHex = "0000000000000000"

func TestIsModuleViewWatcherRequiresRootAndPipe(t *testing.T) {
	if os.Getuid() != 0 && isModuleViewWatcher() {
		t.Fatal("isModuleViewWatcher accepted an unprivileged caller")
	}
}

func TestProtectedLaunchBinariesBuildWithoutCgo(t *testing.T) {
	t.Parallel()
	stageScript, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "stage-engine.sh"))
	if err != nil {
		t.Fatalf("read scripts/stage-engine.sh: %v", err)
	}
	for _, want := range []string{
		`if [[ "${HOST_KIND}" == "linux" ]]; then`,
		"export CGO_ENABLED=0",
		"assert_static()",
		"INTERP\\|Requesting program interpreter",
	} {
		if !bytes.Contains(stageScript, []byte(want)) {
			t.Fatalf("scripts/stage-engine.sh missing %q required to keep Linux launch binaries static and cgo-free", want)
		}
	}
	pkgs := []string{
		"github.com/lycaon/lycaon/cmd/pw-launch",
		"github.com/lycaon/lycaon/cmd/pw-launch-entry",
	}
	cmd := exec.Command("go", append([]string{"list", "-deps"}, pkgs...)...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %v: %v\n%s", pkgs, err, out)
	}
	if slices.Contains(strings.Fields(string(out)), "runtime/cgo") {
		t.Fatalf("protected launch packages import runtime/cgo, which makes syscall.AllThreadsSyscall fail with ENOTSUP")
	}
	dir := t.TempDir()
	for _, pkg := range pkgs {
		bin := filepath.Join(dir, filepath.Base(pkg))
		goBuild(t, bin, pkg, "")
		info, err := buildinfo.ReadFile(bin)
		if err != nil {
			t.Fatalf("read buildinfo for %s: %v", pkg, err)
		}
		var sawCgoDisabled bool
		for _, setting := range info.Settings {
			if setting.Key == "CGO_ENABLED" {
				if setting.Value != "0" {
					t.Fatalf("%s built with CGO_ENABLED=%s, want 0", pkg, setting.Value)
				}
				sawCgoDisabled = true
			}
		}
		if !sawCgoDisabled {
			t.Fatalf("%s buildinfo did not record CGO_ENABLED=0", pkg)
		}
		elfFile, err := elf.Open(bin)
		if err != nil {
			t.Fatalf("open ELF %s: %v", pkg, err)
		}
		for _, prog := range elfFile.Progs {
			if prog.Type == elf.PT_INTERP {
				_ = elfFile.Close()
				t.Fatalf("%s has PT_INTERP header; protected launch binaries must be static (CGO_ENABLED=0)", pkg)
			}
		}
		_ = elfFile.Close()
	}
}

func checkScrubSingleMountRejectsSymlinksAndMagiclinks(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real", "sub")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir realDir: %v", err)
	}
	symParent := filepath.Join(tmp, "sym-parent")
	if err := os.Symlink(filepath.Join(tmp, "real"), symParent); err != nil {
		t.Fatalf("symlink parent: %v", err)
	}
	for _, bad := range []string{"relative/path", "/mnt/nul\x00byte"} {
		if err := scrubSingleMount(bad); !errors.Is(err, unix.EINVAL) {
			t.Fatalf("scrubSingleMount(%q) = %v, want EINVAL", bad, err)
		}
	}
	for _, linkPath := range []string{filepath.Join(symParent, "sub"), symParent, "/proc/self/root"} {
		if err := scrubSingleMount(linkPath); !errors.Is(err, unix.ELOOP) {
			t.Fatalf("scrubSingleMount(%q) = %v, want ELOOP", linkPath, err)
		}
	}
}

func verifyLiveWatcherProcess(desktopPID int) error {
	desktopMnt, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", desktopPID))
	if err != nil {
		return err
	}
	initUser, err := os.Readlink("/proc/1/ns/user")
	if err != nil {
		return err
	}
	desktopStatus, err := readProcStatusFields(fmt.Sprintf("/proc/%d/status", desktopPID))
	if err != nil {
		return err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	var watchers []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil || !bytes.Equal(cmdline, []byte("pw-launch-entry\x00--broker-agent\x00")) {
			continue
		}
		if mnt, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", pid)); err == nil && mnt == desktopMnt {
			watchers = append(watchers, pid)
		}
	}
	if len(watchers) != 1 {
		return fmt.Errorf("live module-view watchers in %s = %v, want 1", desktopMnt, watchers)
	}
	watcherPID := watchers[0]
	if err := verifyMountNamespaceOwner(desktopPID, watcherPID, initUser); err != nil {
		return err
	}
	userNS, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/user", watcherPID))
	if err != nil {
		return fmt.Errorf("watcher user namespace: %w", err)
	}
	if userNS == initUser {
		return fmt.Errorf("watcher user namespace = %q, must not be initial", userNS)
	}
	info, err := os.Stat(fmt.Sprintf("/proc/%d/fd", watcherPID))
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Gid != 0 {
		return fmt.Errorf("watcher /proc/%d/fd owner = %d:%d, want 0:0 (non-dumpable)", watcherPID, stat.Uid, stat.Gid)
	}
	return verifyWatcherThreads(watcherPID, desktopStatus["Uid"], desktopStatus["Gid"], desktopStatus["Groups"])
}

func verifyWatcherThreads(pid int, wantUID, wantGID, wantGroups string) error {
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return err
	}
	if len(tasks) < 2 {
		return fmt.Errorf("watcher pid %d task count = %d, want at least 2 threads", pid, len(tasks))
	}
	want := map[string]string{
		"Uid":        wantUID,
		"Gid":        wantGID,
		"Groups":     wantGroups,
		"CapInh":     wantZeroCapHex,
		"CapPrm":     wantWatcherCapHex,
		"CapEff":     wantWatcherCapHex,
		"CapBnd":     wantWatcherCapHex,
		"CapAmb":     wantZeroCapHex,
		"NoNewPrivs": "1",
		"Seccomp":    "0",
	}
	for _, task := range tasks {
		fields, err := readProcStatusFields(fmt.Sprintf("/proc/%d/task/%s/status", pid, task.Name()))
		if err != nil {
			return err
		}
		for key, expected := range want {
			if got := fields[key]; got != expected {
				return fmt.Errorf("watcher pid %d tid %s %s = %q, want %q", pid, task.Name(), key, got, expected)
			}
		}
	}
	return nil
}

func readProcStatusFields(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fields := make(map[string]string)
	for line := range strings.SplitSeq(string(raw), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields[key] = strings.Join(strings.Fields(val), " ")
	}
	return fields, nil
}

func verifyMountNamespaceOwner(desktopPID, watcherPID int, initUser string) error {
	mntFile, err := os.Open(fmt.Sprintf("/proc/%d/ns/mnt", desktopPID))
	if err != nil {
		return err
	}
	defer func() { _ = mntFile.Close() }()
	ownerUserFD, err := unix.IoctlRetInt(int(mntFile.Fd()), unix.NS_GET_USERNS)
	if err != nil {
		return fmt.Errorf("NS_GET_USERNS on desktop mnt namespace: %w", err)
	}
	defer func() { _ = unix.Close(ownerUserFD) }()
	kind, err := unix.IoctlRetInt(ownerUserFD, unix.NS_GET_NSTYPE)
	if err != nil {
		return fmt.Errorf("NS_GET_NSTYPE on the M_view owner: %w", err)
	}
	if kind != unix.CLONE_NEWUSER {
		return fmt.Errorf("M_view owning namespace type = %#x, want CLONE_NEWUSER", kind)
	}
	ownerUID, err := unix.IoctlGetInt(ownerUserFD, unix.NS_GET_OWNER_UID)
	if err != nil {
		return fmt.Errorf("NS_GET_OWNER_UID on the M_view owner: %w", err)
	}
	if ownerUID != 0 {
		return fmt.Errorf("M_view owning user namespace owner UID = %d, want 0", ownerUID)
	}
	parentFD, err := unix.IoctlRetInt(ownerUserFD, unix.NS_GET_PARENT)
	if err != nil {
		return fmt.Errorf("M_view owning user namespace NS_GET_PARENT: %w", err)
	}
	var parentStat, ownerStat, watcherUserStat, desktopUserStat unix.Stat_t
	err = unix.Fstat(parentFD, &parentStat)
	_ = unix.Close(parentFD)
	if err != nil {
		return fmt.Errorf("M_view owning userns parent: %w", err)
	}
	if parentStat.Ino != 0xEFFFFFFD {
		return fmt.Errorf("M_view owning userns parent ino = %#x, want 0xEFFFFFFD (%s)", parentStat.Ino, initUser)
	}
	if err := unix.Fstat(ownerUserFD, &ownerStat); err != nil {
		return err
	}
	watcherUserFile, err := os.Open(fmt.Sprintf("/proc/%d/ns/user", watcherPID))
	if err != nil {
		return err
	}
	defer func() { _ = watcherUserFile.Close() }()
	if err := unix.Fstat(int(watcherUserFile.Fd()), &watcherUserStat); err != nil {
		return err
	}
	desktopUserFile, err := os.Open(fmt.Sprintf("/proc/%d/ns/user", desktopPID))
	if err != nil {
		return err
	}
	defer func() { _ = desktopUserFile.Close() }()
	if err := unix.Fstat(int(desktopUserFile.Fd()), &desktopUserStat); err != nil {
		return err
	}
	if ownerStat.Ino == 0xEFFFFFFD || ownerStat.Ino != watcherUserStat.Ino || ownerStat.Ino == desktopUserStat.Ino {
		return fmt.Errorf("M_view owning userns ino=%d (watcher=%d desktop=%d), want watcher userns != initial and != desktop", ownerStat.Ino, watcherUserStat.Ino, desktopUserStat.Ino)
	}
	return nil
}

func checkModuleWatcherSeal() error {
	uid, _ := strconv.Atoi(os.Getenv("SUDO_UID"))
	gid, _ := strconv.Atoi(os.Getenv("SUDO_GID"))
	if uid <= 0 || gid <= 0 {
		uid, gid = 1000, 1000
	}
	probeDir, err := os.MkdirTemp("", "pw-watcher-seal-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(probeDir) }()
	if err := unix.Mount("tmpfs", probeDir, "tmpfs", 0, "mode=0755"); err != nil {
		return err
	}
	defer func() { _ = unix.Unmount(probeDir, unix.MNT_DETACH) }()
	stopWorkers := startLockedWorkerThreads(2)
	defer stopWorkers()
	caller := Identity{UID: uint32(uid), GID: uint32(gid), Groups: []uint32{uint32(gid)}}
	if err := sealModuleWatcher(caller); err != nil {
		return err
	}
	stopAfter := startLockedWorkerThreads(1)
	defer stopAfter()
	dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		return fmt.Errorf("sealed watcher dumpable: %w", err)
	}
	if dumpable != 0 {
		return fmt.Errorf("sealed watcher dumpable = %d, want 0", dumpable)
	}
	if err := scrubSingleMount(probeDir); err != nil {
		return fmt.Errorf("sealed watcher scrubSingleMount in initial user namespace: %w", err)
	}
	var st unix.Statfs_t
	const wantFlags = unix.ST_NOEXEC | unix.ST_NOSUID | unix.ST_NODEV
	if err := unix.Statfs(probeDir, &st); err != nil {
		return fmt.Errorf("sealed watcher statfs: %w", err)
	}
	if st.Flags&wantFlags != wantFlags {
		return fmt.Errorf("sealed watcher statfs flags = %#x, want %#x", st.Flags, wantFlags)
	}
	wantUID := fmt.Sprintf("%d %d %d %d", uid, uid, uid, uid)
	wantGID := fmt.Sprintf("%d %d %d %d", gid, gid, gid, gid)
	wantGroups := strconv.Itoa(gid)
	return verifyWatcherThreads(os.Getpid(), wantUID, wantGID, wantGroups)
}

// childUsernsCaller is the identity the user-namespace probe seals itself to.
var childUsernsCaller = Identity{UID: 1000, GID: 1000, Groups: []uint32{1000}}

func checkChildUserNamespaceMountSetattrRefused(mountPoint string) error {
	args := []string{"-test.run=^TestChildUserNamespaceMountSetattrRefused$"}
	// A coverage build reports the child's counters beside the fixture's own.
	// The child exits sealed to childUsernsCaller without CAP_DAC_OVERRIDE, so
	// it writes into a directory that identity owns and the fixture moves the
	// files up afterwards.
	childCover := ""
	if coverDir := os.Getenv("GOCOVERDIR"); testing.CoverMode() != "" && coverDir != "" {
		dir, err := os.MkdirTemp(coverDir, "userns-child-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		if err := os.Chown(dir, int(childUsernsCaller.UID), int(childUsernsCaller.GID)); err != nil {
			return err
		}
		childCover = dir
		args = append(args, "-test.gocoverdir="+dir)
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "PW_WATCHER_CHILD_USERNS_MOUNT="+mountPoint)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 unix.CLONE_NEWUSER,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
		GidMappingsEnableSetgroups: true,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("child userns mount_setattr probe failed: %w: %s", err, out)
	}
	if childCover == "" {
		return nil
	}
	return collectChildCoverage(childCover)
}

// collectChildCoverage moves the probe's coverage files into the shared
// children directory that dir was created under.
func collectChildCoverage(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.Rename(filepath.Join(dir, entry.Name()), filepath.Join(filepath.Dir(dir), entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func TestChildUserNamespaceMountSetattrRefused(t *testing.T) {
	mountPoint := os.Getenv("PW_WATCHER_CHILD_USERNS_MOUNT")
	if mountPoint == "" {
		return
	}
	runtime.LockOSThread()
	// Before unsharing M_view, mountPoint is owned by init_user_ns: the child
	// user namespace has zero capabilities in init_user_ns, so mount_setattr
	// is refused with EPERM even as uid 0 in the child user namespace.
	if err := scrubSingleMount(mountPoint); !errors.Is(err, unix.EPERM) {
		t.Fatalf("scrubSingleMount(%q) on init_user_ns mount from child userns = %v, want EPERM", mountPoint, err)
	}
	// Unshare M_view inside the child user namespace so this userns owns M_view.
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		t.Fatalf("unshare M_view in child userns: %v", err)
	}
	if err := watcherUserNamespace(); err != nil {
		t.Fatalf("watcherUserNamespace in (U_view, M_view): %v", err)
	}
	caller := childUsernsCaller
	if err := sealModuleWatcher(caller); err != nil {
		t.Fatalf("sealModuleWatcher in (U_view, M_view): %v", err)
	}
	// Inside M_view (owned by U_view), the sealed watcher can add NOEXEC|NOSUID|NODEV.
	if err := scrubSingleMount(mountPoint); err != nil {
		t.Fatalf("scrubSingleMount(%q) in watcher-owned M_view: %v", mountPoint, err)
	}
	var st unix.Statfs_t
	const wantFlags = unix.ST_NOEXEC | unix.ST_NOSUID | unix.ST_NODEV
	if err := unix.Statfs(mountPoint, &st); err != nil || st.Flags&wantFlags != wantFlags {
		t.Fatalf("statfs(%q) in watcher-owned M_view = %#x (%v), want %#x", mountPoint, st.Flags, err, wantFlags)
	}
}

func startLockedWorkerThreads(count int) func() {
	ready := make(chan struct{}, count)
	release := make(chan struct{})
	done := make(chan struct{}, count)
	for range count {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			ready <- struct{}{}
			<-release
			done <- struct{}{}
		}()
	}
	for range count {
		<-ready
	}
	return func() {
		close(release)
		for range count {
			<-done
		}
	}
}

func checkInstrumentedBrokerAgent(t *testing.T, spec EntrySpec, frame *os.File) {
	t.Helper()
	server, peer, err := brokerPair()
	testutil.FailErr(t, "instrumented broker socket", err)
	defer peer.Close()
	defer server.Close()
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	testutil.FailErr(t, "broker streams", err)
	defer null.Close()
	cmd := moduleFixtureCommand(t, "module-agent", Identity{UID: spec.UID, GID: spec.GID})
	cmd.ExtraFiles = []*os.File{frame, server, null}
	cmd.Stderr = os.Stderr
	testutil.FailErr(t, "start instrumented broker", cmd.Start())
	_ = server.Close()
	channel, err := launchbroker.Send(peer, launchbroker.Request{Argv: []string{"/bin/true"}, Dir: "/", Environment: []string{"PATH=/usr/bin:/bin"}}, null, null, null)
	testutil.FailErr(t, "instrumented broker request", err)
	defer channel.Close()
	started := moduleFixtureBrokerEvent(t, channel)
	if started.Started <= 0 || started.Error != "" || started.Exited {
		t.Fatalf("instrumented broker started event: %+v", started)
	}
	ended := moduleFixtureBrokerEvent(t, channel)
	if !ended.Exited || ended.Code != 0 || ended.Error != "" {
		t.Fatalf("instrumented broker exit event: %+v", ended)
	}
	_ = peer.Close()
	testutil.FailErr(t, "instrumented broker ended", cmd.Wait())
}

func checkInstrumentedModuleWatcher(t *testing.T, dir string, spec EntrySpec, frame *os.File) {
	t.Helper()
	lifetimeR, lifetime, err := os.Pipe()
	testutil.FailErr(t, "instrumented watcher lifetime", err)
	defer lifetimeR.Close()
	defer lifetime.Close()
	ready, readyW, err := os.Pipe()
	testutil.FailErr(t, "instrumented watcher readiness", err)
	defer ready.Close()
	defer readyW.Close()
	image, err := os.Open(filepath.Join(spec.Generation.GenerationRoot, "bin/pw-launch-entry"))
	testutil.FailErr(t, "watcher retained image", err)
	defer image.Close()
	cmd := moduleFixtureCommand(t, "module-agent", Identity{UID: spec.UID, GID: spec.GID})
	cmd.ExtraFiles = []*os.File{frame, lifetimeR, readyW, image}
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
		GidMappingsEnableSetgroups: true,
	}
	testutil.FailErr(t, "start instrumented watcher", cmd.Start())
	_ = lifetimeR.Close()
	_ = readyW.Close()
	testutil.FailErr(t, "watcher ready deadline", ready.SetReadDeadline(moduleFixtureDeadline(t)))
	var armed [1]byte
	_, err = io.ReadFull(ready, armed[:])
	testutil.FailErr(t, "instrumented watcher ready", err)
	if armed[0] != 1 {
		t.Fatalf("watcher ready byte = %v, want 1", armed)
	}
	uid := fmt.Sprintf("%d %d %d %d", spec.UID, spec.UID, spec.UID, spec.UID)
	gid := fmt.Sprintf("%d %d %d %d", spec.GID, spec.GID, spec.GID, spec.GID)
	testutil.FailErr(t, "instrumented watcher thread seal", verifyWatcherThreads(cmd.Process.Pid, uid, gid, strconv.Itoa(int(spec.GID))))
	checkModuleWatcherPollAuthority(t, cmd.Process.Pid)
	parentNS, err := os.Open("/proc/thread-self/ns/mnt")
	testutil.FailErr(t, "fixture parent mount namespace", err)
	defer parentNS.Close()
	viewNS, err := os.Open(fmt.Sprintf("/proc/%d/ns/mnt", cmd.Process.Pid))
	testutil.FailErr(t, "instrumented watcher view", err)
	defer viewNS.Close()
	library := filepath.Join(dir, "actor-library-root")
	baseline := filepath.Join(library, "baseline.so")
	testutil.FailErr(t, "parent baseline module executable", canMapExecutable(baseline))
	late, _, file, err := prepareLateMountPoint(filepath.Join(library, "instrumented-late"))
	testutil.FailErr(t, "new module mount point", err)
	testutil.FailErr(t, "new executable module mount", mountExecutableTmpfs(late, file))
	testutil.FailErr(t, "parent module remains executable", canMapExecutable(file))
	testutil.FailErr(t, "enter instrumented watcher view", unix.Setns(int(viewNS.Fd()), unix.CLONE_NEWNS))
	defer func() { _ = unix.Setns(int(parentNS.Fd()), unix.CLONE_NEWNS) }()
	testutil.FailErr(t, "declared baseline library remains executable", canMapExecutable(baseline))
	testutil.WaitFor(t, func() bool {
		if !time.Now().Before(moduleFixtureDeadline(t)) {
			t.Fatal("new module mount was not scrubbed before fixture cancellation")
		}
		var facts unix.Statfs_t
		const flags = unix.ST_NOEXEC | unix.ST_NOSUID | unix.ST_NODEV
		return unix.Statfs(late, &facts) == nil && facts.Flags&flags == flags
	})
	if err := canMapExecutable(file); !errors.Is(err, unix.EPERM) {
		t.Fatalf("new module executable mapping in watcher view = %v, want EPERM", err)
	}
	testutil.FailErr(t, "leave instrumented watcher view", unix.Setns(int(parentNS.Fd()), unix.CLONE_NEWNS))
	testutil.FailErr(t, "parent module mount unchanged", canMapExecutable(file))
	_ = lifetime.Close()
	testutil.FailErr(t, "instrumented watcher ended", cmd.Wait())
}

func moduleFixtureBrokerEvent(t *testing.T, channel *os.File) launchbroker.Event {
	t.Helper()
	type result struct {
		event launchbroker.Event
		err   error
	}
	done := make(chan result, 1)
	go func() { event, err := launchbroker.ReadEvent(channel); done <- result{event, err} }()
	timer := time.NewTimer(time.Until(moduleFixtureDeadline(t)))
	defer timer.Stop()
	select {
	case got := <-done:
		testutil.FailErr(t, "module fixture broker event", got.err)
		return got.event
	case <-timer.C:
		t.Fatal("module fixture canceled before broker event")
		return launchbroker.Event{}
	}
}
