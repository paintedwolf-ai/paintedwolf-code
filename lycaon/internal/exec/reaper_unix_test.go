//go:build unix

package exec

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// reaperRoleEnv selects which part a re-executed test binary plays.
const reaperRoleEnv = "PW_TEST_REAPER_ROLE"

// startGroup starts a leader in its own process group with one background
// descendant, returning both pids once the descendant has reported itself.
func startGroup(t *testing.T) (leader, descendant int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	cmd := exec.Command("sh", "-c", fmt.Sprintf("sleep 30 & echo $! > %s; sleep 30", strconv.Quote(pidFile)))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	testutil.FailErr(t, "start group leader", cmd.Start())
	go func() { _ = cmd.Wait() }()
	descendant = waitForPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return cmd.Process.Pid, descendant
}

func reapLines(lines ...string) {
	reap(strings.NewReader(strings.Join(lines, "")))
}

func TestReaperKillsTrackedGroupWhenEngineInputCloses(t *testing.T) {
	leader, descendant := startGroup(t)
	reapLines(fmt.Sprintf("+g%d\n", leader))
	for _, pid := range []int{leader, descendant} {
		if err := waitForProcessGone(pid, 5*time.Second); err != nil {
			testutil.FailErr(t, "tracked group outlived the engine", err)
		}
	}
}

func TestReaperKillsATrackedSingleProcessOnly(t *testing.T) {
	leader, descendant := startGroup(t)
	reapLines(fmt.Sprintf("+p%d\n", descendant))
	if err := waitForProcessGone(descendant, 5*time.Second); err != nil {
		testutil.FailErr(t, "tracked process outlived the engine", err)
	}
	if err := waitForProcessGone(leader, 200*time.Millisecond); err == nil {
		t.Fatal("reaping one tracked process took its untracked group leader with it")
	}
}

func TestReaperSparesForgottenEntries(t *testing.T) {
	leader, descendant := startGroup(t)
	reapLines(fmt.Sprintf("+g%d\n", leader), fmt.Sprintf("-g%d\n", leader), "garbage\n", "+g-4\n")
	for _, pid := range []int{leader, descendant} {
		if err := waitForProcessGone(pid, 200*time.Millisecond); err == nil {
			t.Fatalf("process %d was killed after the engine forgot its group", pid)
		}
	}
}

func TestReaperHoldsAGroupTrackedTwiceUntilBothForgetIt(t *testing.T) {
	leader, _ := startGroup(t)
	reapLines(fmt.Sprintf("+g%d\n", leader), fmt.Sprintf("+g%d\n", leader), fmt.Sprintf("-g%d\n", leader))
	if err := waitForProcessGone(leader, 5*time.Second); err != nil {
		testutil.FailErr(t, "group with one live registration survived", err)
	}
}

// A SIGKILLed engine runs no cleanup of its own; only the reaper ends its groups.
func TestEngineKilledOutrightTakesItsTrackedGroupsWithIt(t *testing.T) {
	dir := t.TempDir()
	self, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)
	engine := exec.Command(self, "-test.run=^TestReaperHelperProcess$", "-test.v=false")
	engine.Env = append(os.Environ(), reaperRoleEnv+"=engine:"+dir)
	engine.Stdout, engine.Stderr = io.Discard, io.Discard
	testutil.FailErr(t, "start engine helper", engine.Start())
	t.Cleanup(func() { _ = engine.Process.Kill(); _ = engine.Wait() })

	leader := waitForPIDFile(t, filepath.Join(dir, "leader.pid"))
	descendant := waitForPIDFile(t, filepath.Join(dir, "descendant.pid"))
	t.Cleanup(func() { _ = syscall.Kill(-leader, syscall.SIGKILL) })
	waitForPIDFile(t, filepath.Join(dir, "tracked.pid"))

	testutil.FailErr(t, "SIGKILL engine helper", engine.Process.Kill())
	_ = engine.Wait()
	for _, pid := range []int{leader, descendant} {
		if err := waitForProcessGone(pid, 5*time.Second); err != nil {
			testutil.FailErr(t, "tracked group outlived a SIGKILLed engine", err)
		}
	}
}

// TestReaperHelperProcess plays the engine or its reaper, not a test. It runs
// only when the parent case re-execs this binary with reaperRoleEnv set.
func TestReaperHelperProcess(t *testing.T) {
	role, dir, _ := strings.Cut(os.Getenv(reaperRoleEnv), ":")
	switch role {
	case "reaper":
		os.Exit(RunReaper(os.Stdin))
	case "engine":
	default:
		t.Skip("helper process for TestEngineKilledOutrightTakesItsTrackedGroupsWithIt")
	}
	// The companion inherits this environment and so takes the reaper role.
	t.Setenv(reaperRoleEnv, "reaper")
	self, err := os.Executable()
	if err == nil {
		err = StartReaper(self, "-test.run=^TestReaperHelperProcess$", "-test.v=false")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "start reaper: %v\n", err)
		os.Exit(1)
	}
	script := fmt.Sprintf("echo $$ > %s; sleep 30 & echo $! > %s; sleep 30",
		strconv.Quote(filepath.Join(dir, "leader.pid")), strconv.Quote(filepath.Join(dir, "descendant.pid")))
	// StartPipelineAsync returns once the group is started and tracked.
	if _, err := StartPipelineAsync(context.Background(), []Stage{{Name: "sh", Args: []string{"-c", script}}},
		ExecOpts{Launch: HostLaunch("reaper test"), NoTimeout: true}, nil, nil); err != nil {
		fmt.Fprintf(os.Stderr, "start pipeline: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(1)
	}
	time.Sleep(90 * time.Second)
	os.Exit(0)
}
