//go:build linux

package exec

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/testutil"
)

// startTreeRoot starts a shell standing in for the supervisor: its descendants
// are the tree under test. Each line the script prints is a descendant's pid.
func startTreeRoot(t *testing.T, script string, pids int) (*exec.Cmd, []int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	testutil.FailErr(t, "pipe tree root output", err)
	testutil.FailErr(t, "start tree root", cmd.Start())
	t.Cleanup(func() {
		// A reaped root's group id may already name someone else's group.
		if cmd.ProcessState == nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	})
	lines := bufio.NewScanner(out)
	var started []int
	for len(started) < pids && lines.Scan() {
		pid, err := strconv.Atoi(strings.TrimSpace(lines.Text()))
		testutil.FailErr(t, "read descendant pid", err)
		started = append(started, pid)
	}
	if len(started) < pids {
		t.Fatalf("tree root reported %d of %d descendants", len(started), pids)
	}
	return cmd, started
}

func descendantPIDs(root int) map[int]bool {
	pids := map[int]bool{}
	for _, child := range commandDescendants(root) {
		pids[child.pid] = true
	}
	return pids
}

func TestParseProcStatReadsParentAndStartAfterTheCommandName(t *testing.T) {
	tail := " S 41 7 7 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 98765 1000"
	parent, start, ok := parseProcStat([]byte("42 (odd) name) here)" + tail))
	if !ok || parent != 41 || start != 98765 {
		t.Fatalf("parse = %d/%d/%v, want parent 41 start 98765", parent, start, ok)
	}
	for name, raw := range map[string]string{
		"no command name":  "42 odd" + tail,
		"truncated fields": "42 (odd) S 41 7",
		"parent not a pid": "42 (odd) S x 7 7 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 98765 1000",
		"start not a tick": "42 (odd) S 41 7 7 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 soon 1000",
	} {
		if _, _, ok := parseProcStat([]byte(raw)); ok {
			t.Errorf("%s: parsed malformed stat %q", name, raw)
		}
	}
}

func stopped(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	fields := bytes.Fields(raw[bytes.LastIndexByte(raw, ')')+1:])
	return len(fields) > 0 && string(fields[0]) == "T"
}

func waitForStopped(pid int, within time.Duration) bool {
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if stopped(pid) {
			return true
		}
	}
	return false
}

func TestCommandDescendantsFollowsTheWholeTreeOnly(t *testing.T) {
	other, otherChild := startTreeRoot(t, `sleep 30 & echo $!; wait`, 1)
	root, direct := startTreeRoot(t, `sh -c 'sleep 30 & echo $!; wait' & echo $!; wait`, 2)
	tree := descendantPIDs(root.Process.Pid)
	for _, pid := range direct {
		if !tree[pid] || !descendantOf(pid, root.Process.Pid) {
			t.Fatalf("descendant %d missing from tree %v of root %d", pid, tree, root.Process.Pid)
		}
	}
	if tree[other.Process.Pid] || descendantOf(other.Process.Pid, root.Process.Pid) {
		t.Fatalf("unrelated process %d claimed by root %d", other.Process.Pid, root.Process.Pid)
	}
	if descendantOf(root.Process.Pid, direct[1]) || descendantOf(1<<22+1, root.Process.Pid) {
		t.Fatal("ancestry ran backwards or through a missing process")
	}
	if !descendantOf(root.Process.Pid, os.Getpid()) {
		t.Fatalf("root %d not a descendant of the test %d", root.Process.Pid, os.Getpid())
	}
	// SIGSTOP leaves the lineage intact: a killed intermediate would orphan its
	// child out of this tree, since only the real supervisor is a subreaper.
	if n := signalCommandDescendants(root.Process.Pid, syscall.SIGSTOP); n != 2 {
		t.Fatalf("signalled %d descendants, want 2", n)
	}
	for _, pid := range direct {
		if !waitForStopped(pid, 5*time.Second) {
			t.Fatalf("descendant %d was not signalled", pid)
		}
	}
	if stopped(root.Process.Pid) || stopped(other.Process.Pid) || stopped(otherChild[0]) {
		t.Fatal("signalling a tree reached its root or an unrelated tree")
	}
}

// ignoresTerm prints its pid only after it ignores SIGTERM.
const ignoresTerm = `sh -c 'trap "" TERM; echo $$; exec sleep 30' & wait`

func TestSupervisionSettlesTheTree(t *testing.T) {
	sentinel := errors.New("primary result")
	cases := []struct {
		name   string
		script string
		grace  time.Duration
		// fakePrimary reports the primary finished at once; otherwise the
		// root itself is the primary and finishes when its tree is gone.
		fakePrimary bool
		events      []os.Signal
		loseEngine  time.Duration // <0 before settling; >0 after that delay
		minElapsed  time.Duration
	}{
		{name: "primary completion terminates descendants", script: `sleep 30 & echo $!; wait`, grace: time.Minute, fakePrimary: true},
		{name: "grace escalates to kill", script: ignoresTerm, grace: 200 * time.Millisecond, fakePrimary: true, minElapsed: 200 * time.Millisecond},
		{name: "engine loss kills before the primary finishes", script: ignoresTerm, grace: time.Minute, loseEngine: -1},
		{name: "terminate request then kill request", script: ignoresTerm, grace: time.Minute, events: []os.Signal{syscall.SIGTERM, syscall.SIGUSR2}},
		{name: "terminate request then engine loss", script: ignoresTerm, grace: time.Minute, events: []os.Signal{syscall.SIGTERM}, loseEngine: 100 * time.Millisecond},
		{name: "kill request before the primary finishes", script: ignoresTerm, grace: time.Minute, events: []os.Signal{syscall.SIGUSR2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, pids := startTreeRoot(t, tc.script, 1)
			finished := make(chan error, 1)
			if tc.fakePrimary {
				finished <- sentinel
			} else {
				go func() { finished <- root.Wait() }()
			}
			events := make(chan os.Signal, len(tc.events))
			if len(tc.events) > 0 {
				events <- tc.events[0]
				go func() {
					for _, sig := range tc.events[1:] {
						time.Sleep(100 * time.Millisecond)
						events <- sig
					}
				}()
			}
			gone := make(chan struct{})
			if tc.loseEngine < 0 {
				close(gone)
			} else if tc.loseEngine > 0 {
				time.AfterFunc(tc.loseEngine, func() { close(gone) })
			}
			reaps := 0
			began := time.Now()
			result := supervision{
				root: root.Process.Pid, finished: finished, events: events, gone: gone,
				grace: tc.grace, reap: func() { reaps++ },
			}.settle()
			elapsed := time.Since(began)
			if tc.fakePrimary && !errors.Is(result, sentinel) {
				t.Fatalf("settle result = %v, want the primary's result", result)
			}
			if reaps == 0 || elapsed < tc.minElapsed || elapsed > 20*time.Second {
				t.Fatalf("reaps=%d elapsed=%v, want reaping within [%v, 20s)", reaps, elapsed, tc.minElapsed)
			}
			testutil.FailErr(t, "descendant survived settlement", waitForProcessGone(pids[0], 5*time.Second))
		})
	}
}

func TestSupervisorExitPreservesThePrimaryStatus(t *testing.T) {
	exited := exec.Command("sh", "-c", "exit 3").Run()
	killed := exec.Command("sh", "-c", "kill -KILL $$").Run()
	cases := []struct {
		name   string
		result error
		code   int
		sig    syscall.Signal
	}{
		{"success", nil, 0, 0},
		{"exit status", exited, 3, 0},
		{"killed by a signal", killed, 128 + int(syscall.SIGKILL), syscall.SIGKILL},
		{"wait failure", errors.New("wait failed"), 125, 0},
	}
	for _, tc := range cases {
		if code, sig := supervisorExit(tc.result); code != tc.code || sig != tc.sig {
			t.Errorf("%s: exit = %d/%v, want %d/%v", tc.name, code, sig, tc.code, tc.sig)
		}
	}
}

func TestParseSupervisorTargetRequiresDescriptorCountAndCommand(t *testing.T) {
	target, ok := parseSupervisorTarget([]string{"2", string(ProcessPriorityBelowNormal), "/bin/sh", "sh", "-c", "true"})
	if !ok || target.extraFiles != 2 || target.priority != ProcessPriorityBelowNormal || target.path != "/bin/sh" ||
		strings.Join(target.args, " ") != "sh -c true" {
		t.Fatalf("target = %+v/%v", target, ok)
	}
	for _, args := range [][]string{{"0", "", "/bin/sh"}, {"x", "", "/bin/sh", "sh"}, {"-1", "", "/bin/sh", "sh"}} {
		if _, ok := parseSupervisorTarget(args); ok {
			t.Errorf("accepted malformed supervisor arguments %q", args)
		}
	}
}

func TestSupervisorLaunchReportReachesTheParent(t *testing.T) {
	const missing = "/nonexistent/supervised-target"
	launch := func(t *testing.T, started error, report bool) error {
		t.Helper()
		read, status, err := os.Pipe()
		testutil.FailErr(t, "open startup status pipe", err)
		cmd := &exec.Cmd{Args: []string{"engine", supervisorCommand, "0", "", missing}, ExtraFiles: []*os.File{status}}
		supervisorStarts.Store(cmd, read)
		if report {
			testutil.FailErr(t, "report launch", reportLaunch(status, started))
			if reportLaunch(status, started) == nil {
				t.Fatal("a report to a closed status pipe claimed delivery")
			}
		}
		return supervisorStartupError(cmd)
	}
	notFound := startSupervisorTarget(&exec.Cmd{Path: missing, Args: []string{"target"}}, ProcessPriorityNormal)
	var pathErr *os.PathError
	if err := launch(t, notFound, true); !errors.As(err, &pathErr) || pathErr.Path != missing || !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("missing target startup = %v, want the exec ENOENT for %s", err, missing)
	}
	if err := launch(t, errors.New("no errno"), true); !errors.Is(err, syscall.EIO) {
		t.Fatalf("errno-less startup failure = %v, want EIO", err)
	}
	if err := launch(t, nil, true); err != nil {
		t.Fatalf("started target reported %v", err)
	}
	if err := launch(t, nil, false); err == nil {
		t.Fatal("a supervisor that never reported was treated as started")
	}
}

func TestAwaitEngineLossFollowsTheLifetimePipe(t *testing.T) {
	read, write, err := os.Pipe()
	testutil.FailErr(t, "open lifetime pipe", err)
	gone := awaitEngineLoss(read)
	select {
	case <-gone:
		t.Fatal("reported engine loss while the engine held the pipe")
	case <-time.After(50 * time.Millisecond):
	}
	_ = write.Close()
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("engine loss was not observed")
	}
}

func TestStartSupervisorTargetAppliesTheRequestedPriority(t *testing.T) {
	inherited, err := processNice(os.Getpid())
	testutil.FailErr(t, "read inherited priority", err)
	for priority, want := range map[ProcessPriority]int{
		ProcessPriorityNormal:      inherited,
		ProcessPriorityBelowNormal: max(inherited, DefaultBelowNormalNice),
	} {
		cmd := exec.Command("sleep", "30")
		started := make(chan error, 1)
		go func() {
			// The renice applies to this thread, which exits with the goroutine.
			runtime.LockOSThread()
			started <- startSupervisorTarget(cmd, priority)
		}()
		testutil.FailErr(t, "start supervised target", <-started)
		nice, err := processNice(cmd.Process.Pid)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		testutil.FailErr(t, "read target priority", err)
		if nice != want {
			t.Errorf("%q target nice = %d, want %d", priority, nice, want)
		}
	}
}

func TestSignalSupervisorRefusesAReplacedProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	testutil.FailErr(t, "start signal target", cmd.Start())
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	start, alive := osprocess.StartTime(cmd.Process.Pid)
	if !alive {
		t.Fatal("signal target exited early")
	}
	if signalSupervisor(cmd.Process.Pid, start+1, false) {
		t.Fatal("signalled a process whose start time no longer matches")
	}
	if !signalSupervisor(cmd.Process.Pid, start, false) {
		t.Fatal("did not signal the recorded supervisor")
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("terminated supervisor exited cleanly")
	}
}
