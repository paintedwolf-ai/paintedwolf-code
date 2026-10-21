//go:build linux

package linuxsandbox

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
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

func TestStatusFilesReadTheCurrentRecordEachTime(t *testing.T) {
	statuses := newStatusFiles()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	tid := unix.Gettid()
	old := unix.Umask(0o027)
	defer unix.Umask(old)
	first, err := statuses.facts(tid)
	testutil.FailErr(t, "first facts", err)
	unix.Umask(0o077)
	second, err := statuses.facts(tid)
	testutil.FailErr(t, "second facts", err)
	if first.umask != 0o027 || second.umask != 0o077 || second.tgid != os.Getpid() {
		t.Fatalf("cached status read %o then %o (tgid %d), want 027 then 077", first.umask, second.umask, second.tgid)
	}
	if len(statuses.byTID) != 1 {
		t.Fatalf("status files cached: %d", len(statuses.byTID))
	}
	if _, err := statuses.facts(-1); err == nil {
		t.Fatal("read facts for no thread")
	}
}

func TestCallerFactsParseEachField(t *testing.T) {
	facts, err := parseCallerFacts([]byte("Name:\tx\nUmask:\t0022\nTgid:\t40\nNStgid:\t40\t7\nNSpid:\t41\t9\n"))
	testutil.FailErr(t, "parse", err)
	if facts != (callerFacts{tgid: 40, nsPid: 7, nsTid: 9, umask: 0o022}) {
		t.Fatalf("facts = %+v", facts)
	}
	if facts, err := parseCallerFacts([]byte("Name:\tx\n")); err == nil || facts.umask != 0o777 {
		t.Fatalf("record without a thread group: %+v %v", facts, err)
	}
}
