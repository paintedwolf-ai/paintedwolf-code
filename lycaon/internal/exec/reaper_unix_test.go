//go:build unix

package exec

import (
	"context"
	"errors"
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

	"github.com/lycaon/lycaon/internal/osprocess"
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
		if err := waitForProcessGone(t.Context(), pid); err != nil {
			testutil.FailErr(t, "tracked group outlived the engine", err)
		}
	}
}

func TestReaperKillsATrackedSingleProcessOnly(t *testing.T) {
	leader, descendant := startGroup(t)
	reapLines(fmt.Sprintf("+p%d\n", descendant))
	if err := waitForProcessGone(t.Context(), descendant); err != nil {
		testutil.FailErr(t, "tracked process outlived the engine", err)
	}
	if !osprocess.Alive(leader) {
		t.Fatal("reaping one tracked process took its untracked group leader with it")
	}
}

func TestReaperSparesForgottenEntries(t *testing.T) {
	leader, descendant := startGroup(t)
	reapLines(fmt.Sprintf("+g%d\n", leader), fmt.Sprintf("-g%d\n", leader), "garbage\n", "+g-4\n")
	for _, pid := range []int{leader, descendant} {
		if !osprocess.Alive(pid) {
			t.Fatalf("process %d was killed after the engine forgot its group", pid)
		}
	}
}

func TestReaperHoldsAGroupTrackedTwiceUntilBothForgetIt(t *testing.T) {
	leader, _ := startGroup(t)
	reapLines(fmt.Sprintf("+g%d\n", leader), fmt.Sprintf("+g%d\n", leader), fmt.Sprintf("-g%d\n", leader))
	if err := waitForProcessGone(t.Context(), leader); err != nil {
		testutil.FailErr(t, "group with one live registration survived", err)
	}
}

func TestReaperKillsSurvivingSessionMembersAfterLeaderExitsAndSparesMismatchedStart(t *testing.T) {
	dir := t.TempDir()
	memberPIDFile := filepath.Join(dir, "member.pid")
	grandchildPIDFile := filepath.Join(dir, "grandchild.pid")

	script := fmt.Sprintf(
		"set -m; sh -c 'echo $$ > %s; sleep 30 & echo $! > %s; wait' & exit 0",
		strconv.Quote(memberPIDFile), strconv.Quote(grandchildPIDFile),
	)
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	testutil.FailErr(t, "start session leader", cmd.Start())
	leaderInc, ok := osprocess.IncarnationOf(cmd.Process.Pid)
	if !ok {
		t.Fatal("failed to read leader incarnation")
	}

	memberPID := waitForPIDFile(t, memberPIDFile)
	grandchildPID := waitForPIDFile(t, grandchildPIDFile)
	t.Cleanup(func() {
		_ = syscall.Kill(memberPID, syscall.SIGKILL)
		_ = syscall.Kill(grandchildPID, syscall.SIGKILL)
	})
	memberInc, ok := osprocess.IncarnationOf(memberPID)
	if !ok {
		t.Fatal("failed to read surviving member incarnation")
	}
	testutil.FailErr(t, "reap exited leader", cmd.Wait())

	bystander := exec.Command("cat")
	stdin, err := bystander.StdinPipe()
	testutil.FailErr(t, "bystander stdin", err)
	defer func() { _ = stdin.Close() }()
	testutil.FailErr(t, "start bystander", bystander.Start())
	defer func() {
		if bystander.ProcessState == nil {
			_ = bystander.Process.Kill()
			_ = bystander.Wait()
		}
	}()
	bystanderInc, ok := osprocess.IncarnationOf(bystander.Process.Pid)
	if !ok {
		t.Fatal("failed to read bystander incarnation")
	}

	// Leader has exited and been forgotten (-g), while memberInc was transferred
	// before leader reap and a stale registration for bystander's PID has a
	// mismatched start timestamp.
	reapLines(
		fmt.Sprintf("+g%d:%d\n", leaderInc.PID, leaderInc.Start),
		fmt.Sprintf("+m%d:%d:%d\n", leaderInc.PID, memberInc.PID, memberInc.Start),
		fmt.Sprintf("-g%d\n", leaderInc.PID),
		fmt.Sprintf("+p%d:%d\n", bystanderInc.PID, bystanderInc.Start-1),
		fmt.Sprintf("+m%d:%d:%d\n", leaderInc.PID, bystanderInc.PID, bystanderInc.Start-1),
	)

	for _, pid := range []int{memberPID, grandchildPID} {
		if err := waitForProcessGone(t.Context(), pid); err != nil {
			testutil.FailErr(t, "surviving session member outlived reaper", err)
		}
	}
	if !bystanderInc.Alive() {
		t.Fatal("reaper killed process whose start time differed from its registration")
	}
}

func TestReaperRemovesTrackedDirAfterKillingItsGroup(t *testing.T) {
	leader, descendant := startGroup(t)
	scratch := filepath.Join(t.TempDir(), "scratch")
	testutil.FailErr(t, "create scratch", os.MkdirAll(filepath.Join(scratch, "nested"), 0o700))
	testutil.FailErr(t, "write scratch file", os.WriteFile(filepath.Join(scratch, "nested", "report.json"), []byte("{}"), 0o600))
	reapLines(fmt.Sprintf("+g%d\n", leader), "+d"+scratch+"\n")
	for _, pid := range []int{leader, descendant} {
		if err := waitForProcessGone(t.Context(), pid); err != nil {
			testutil.FailErr(t, "tracked group outlived the engine", err)
		}
	}
	if _, err := os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tracked directory survived the engine: stat err=%v", err)
	}
}

func TestReaperSparesForgottenAndUnanchoredDirs(t *testing.T) {
	root := t.TempDir()
	forgotten := filepath.Join(root, "forgotten")
	testutil.FailErr(t, "create forgotten dir", os.Mkdir(forgotten, 0o700))
	unclean := filepath.Join(root, "unclean")
	testutil.FailErr(t, "create unclean dir", os.Mkdir(unclean, 0o700))
	reapLines("+d"+forgotten+"\n", "-d"+forgotten+"\n", "+d"+root+"/unclean/.\n", "+drelative\n")
	for _, dir := range []string{forgotten, unclean} {
		if _, err := os.Stat(dir); err != nil {
			testutil.FailErr(t, "reaper removed an untracked directory", err)
		}
	}
}

// A SIGKILLed engine runs no cleanup of its own; only the reaper ends its
// groups and removes its scratch directories.
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
		if err := waitForProcessGone(t.Context(), pid); err != nil {
			testutil.FailErr(t, "tracked group outlived a SIGKILLed engine", err)
		}
	}
	scratch := filepath.Join(dir, "scratch")
	for {
		_, err := os.Stat(scratch)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if t.Context().Err() != nil {
			t.Fatalf("tracked directory outlived a SIGKILLed engine: stat err=%v", err)
		}
		time.Sleep(20 * time.Millisecond)
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
	scratch := filepath.Join(dir, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		os.Exit(1)
	}
	TrackScratchDir(scratch)
	if err := os.WriteFile(filepath.Join(dir, "tracked.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(1)
	}
	time.Sleep(90 * time.Second)
	os.Exit(0)
}

// The reaper deletes only directories inside the temp root.
func TestReaperDirPathStaysInsideTheTempRoot(t *testing.T) {
	root := filepath.Clean(os.TempDir())
	for path, want := range map[string]bool{
		filepath.Join(root, "scan-run"):   true,
		root:                              false,
		filepath.Dir(root):                false,
		"/usr/bin":                        false,
		filepath.Join(root, "..", "else"): false,
		"relative/scan-run":               false,
	} {
		if got := reaperDirPath(path); got != want {
			t.Errorf("reaperDirPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestEnsureReaperStartsAndReplacesCompanion(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)
	t.Cleanup(func() {
		reaper.mu.Lock()
		defer reaper.mu.Unlock()
		if reaper.pipe != nil {
			_ = reaper.pipe.Close()
			if reaper.cmd != nil {
				_ = reaper.cmd.Wait()
			}
			reaper.pipe = nil
			reaper.cmd = nil
		}
	})
	testutil.FailErr(t, "ensure reaper", ensureReaper())
	testutil.FailErr(t, "ensure reaper idempotent", ensureReaper())
	testutil.FailErr(t, "replace reaper", StartReaper(self, ReaperCommand))
	if err := StartReaper("/nonexistent/reaper"); err == nil {
		t.Fatal("StartReaper with missing binary succeeded")
	}
	reaper.mu.Lock()
	if reaper.pipe != nil {
		_ = reaper.pipe.Close()
	}
	reaper.mu.Unlock()
	if sendReaper('+', reaperGroup, "1") {
		t.Fatal("sendReaper succeeded on closed pipe")
	}
}
