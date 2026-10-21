//go:build linux

package hostprocess

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

const nonDumpableChildEnv = "PW_HOSTPROCESS_NONDUMPABLE_CHILD"

// TestNonDumpableChildProcess is the body of the non-dumpable fixture; it does
// nothing unless a test started it.
func TestNonDumpableChildProcess(t *testing.T) {
	if os.Getenv(nonDumpableChildEnv) != "1" {
		return
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		os.Exit(2)
	}
	_, _ = os.Stdout.WriteString("ready\n")
	time.Sleep(time.Minute)
	os.Exit(0)
}

func TestNonDumpableProcessesReportTheirOwner(t *testing.T) {
	service, err := New()
	testutil.FailErr(t, "create service", err)
	child := exec.Command(os.Args[0], "-test.run=^TestNonDumpableChildProcess$")
	child.Env = append(os.Environ(), nonDumpableChildEnv+"=1")
	out, err := child.StdoutPipe()
	testutil.FailErr(t, "child output", err)
	testutil.FailErr(t, "start non-dumpable child", child.Start())
	done := make(chan struct{})
	go func() { _ = child.Wait(); close(done) }()
	t.Cleanup(func() { _ = child.Process.Kill(); <-done })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("child readiness: %q %v", line, err)
	}
	var entry unix.Stat_t
	testutil.FailErr(t, "stat process entry", unix.Stat("/proc/"+strconv.Itoa(child.Process.Pid), &entry))
	if os.Geteuid() != 0 && entry.Uid != 0 {
		t.Fatalf("fixture is not non-dumpable: /proc entry owner %d", entry.Uid)
	}
	snapshot, err := service.List(t.Context(), "task", child.Process.Pid, 0, 1)
	if err != nil || len(snapshot.Processes) != 1 {
		t.Fatalf("snapshot child: rows=%v err=%v", snapshot.Processes, err)
	}
	if got := snapshot.Processes[0].UID; got != uint32(os.Geteuid()) {
		t.Fatalf("non-dumpable child uid = %d, want %d", got, os.Geteuid())
	}
	testutil.FailErr(t, "signal non-dumpable child", service.Signal(t.Context(), snapshot.Processes[0], "TERM"))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("non-dumpable child did not exit")
	}
}

func TestEffectiveUIDRefusesMalformedStatus(t *testing.T) {
	dir := t.TempDir()
	for name, status := range map[string]string{
		"missing": "Name:\tx\nGid:\t1 1 1 1\n",
		"short":   "Uid:\t1000 1000\n",
		"text":    "Uid:\t1000 nobody 1000 1000\n",
	} {
		testutil.FailErr(t, "write "+name, os.WriteFile(dir+"/status", []byte(status), 0o600))
		if _, err := effectiveUID(dir); err == nil {
			t.Fatalf("%s status accepted", name)
		}
	}
	testutil.FailErr(t, "write effective", os.WriteFile(dir+"/status", []byte("Uid:\t1000\t1001\t1002\t1003\n"), 0o600))
	if uid, err := effectiveUID(dir); err != nil || uid != 1001 {
		t.Fatalf("effective uid = %d, %v; want 1001", uid, err)
	}
}
