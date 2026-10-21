//go:build linux

package launchguard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const wantWatcherCapHex = "0000000000200004"
const wantZeroCapHex = "0000000000000000"

func TestIsModuleViewWatcherRequiresRootAndPipe(t *testing.T) {
	if os.Getuid() != 0 && isModuleViewWatcher() {
		t.Fatal("isModuleViewWatcher accepted an unprivileged caller")
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
	userNS, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/user", watcherPID))
	if err != nil || userNS != initUser {
		return fmt.Errorf("watcher user namespace = %q (%v), want %q", userNS, err, initUser)
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

func checkModuleWatcherSeal() error {
	uid, _ := strconv.Atoi(os.Getenv("SUDO_UID"))
	gid, _ := strconv.Atoi(os.Getenv("SUDO_GID"))
	if uid <= 0 || gid <= 0 {
		uid, gid = 1000, 1000
	}
	stopWorkers := startLockedWorkerThreads(2)
	defer stopWorkers()
	caller := Identity{UID: uint32(uid), GID: uint32(gid), Groups: []uint32{uint32(gid)}}
	if err := sealModuleWatcher(caller); err != nil {
		return err
	}
	stopAfter := startLockedWorkerThreads(1)
	defer stopAfter()
	if dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0); err != nil || dumpable != 0 {
		return fmt.Errorf("sealed watcher dumpable = %d (%v), want 0", dumpable, err)
	}
	wantUID := fmt.Sprintf("%d %d %d %d", uid, uid, uid, uid)
	wantGID := fmt.Sprintf("%d %d %d %d", gid, gid, gid, gid)
	wantGroups := strconv.Itoa(gid)
	return verifyWatcherThreads(os.Getpid(), wantUID, wantGID, wantGroups)
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
