//go:build linux

package linuxsandbox

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

const execChildEnv = "PW_TARGET_HANDLES_EXEC_CHILD"

// TestTargetHandlesExecChild reports readiness, waits for a line, then
// replaces itself with sleep, leaving its old address space without users.
func TestTargetHandlesExecChild(t *testing.T) {
	if os.Getenv(execChildEnv) == "" {
		return
	}
	_, _ = os.Stdout.WriteString("ready\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	_ = unix.Exec("/bin/sleep", []string{"sleep", "30"}, os.Environ())
	os.Exit(3)
}

func TestTargetHandlesFollowAProcessThroughExecAndExit(t *testing.T) {
	cache := newTargetHandles()
	child := exec.Command(os.Args[0], "-test.run=^TestTargetHandlesExecChild$")
	child.Env = append(os.Environ(), execChildEnv+"=1")
	stdin, err := child.StdinPipe()
	testutil.FailErr(t, "child input", err)
	stdout, err := child.StdoutPipe()
	testutil.FailErr(t, "child output", err)
	testutil.FailErr(t, "start child", child.Start())
	done := make(chan struct{})
	t.Cleanup(func() { _ = child.Process.Kill(); <-done })
	go func() { _ = child.Wait(); close(done) }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("child readiness %q: %v", line, err)
	}
	pid := child.Process.Pid
	first, err := cache.acquire(pid)
	testutil.FailErr(t, "acquire", err)
	again, err := cache.acquire(pid)
	testutil.FailErr(t, "reacquire", err)
	if again != first {
		t.Fatal("a live process's handles were not reused")
	}
	cache.release(again)

	_, err = stdin.Write([]byte("go\n"))
	testutil.FailErr(t, "release child into exec", err)
	var start uint64
	for deadline := time.Now().Add(10 * time.Second); start == 0; {
		if time.Now().After(deadline) {
			t.Fatal("child never executed sleep")
		}
		if exe, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe"); strings.HasSuffix(exe, "/sleep") {
			start = firstMapping(t, pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	buf := make([]byte, 16)
	if n, err := first.readAt(pid, buf, int64(start)); err != nil || n != len(buf) {
		t.Fatalf("read the new address space through a handle opened before exec: %d %v", n, err)
	}
	cache.release(first)

	_ = child.Process.Kill()
	<-done
	if _, err := cache.acquire(pid); err == nil {
		t.Fatal("acquired handles for an exited process")
	}
	if _, ok := cache.byGroup[pid]; ok {
		t.Fatal("an exited process stayed cached")
	}
}

// firstMapping is the start of a readable mapping of pid.
func firstMapping(t *testing.T, pid int) uint64 {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/maps")
	testutil.FailErr(t, "maps", err)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[1], "r") {
			continue
		}
		start, err := strconv.ParseUint(strings.SplitN(fields[0], "-", 2)[0], 16, 64)
		if err == nil {
			return start
		}
	}
	t.Fatal("no readable mapping")
	return 0
}

func TestTargetHandlesBoundTheirCache(t *testing.T) {
	cache := newTargetHandles()
	own, err := cache.acquire(os.Getpid())
	testutil.FailErr(t, "acquire", err)
	cache.release(own)
	for i := 0; i < maxCachedTargets; i++ {
		cache.byGroup[-1-i] = &processHandles{pidfd: -1}
	}
	cache.mu.Lock()
	cache.sweepLocked()
	cache.mu.Unlock()
	if len(cache.byGroup) >= maxCachedTargets {
		t.Fatalf("cache kept %d entries", len(cache.byGroup))
	}
}
