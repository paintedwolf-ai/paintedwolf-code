//go:build linux

package exec

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/osprocess"
)

const supervisorCommand = "__command-supervisor"

var supervisorStarts sync.Map // *exec.Cmd -> *os.File

type supervisorLaunchResult struct {
	Errno int `json:"errno"`
}

func supervisorStartupError(cmd *exec.Cmd) error {
	value, ok := supervisorStarts.LoadAndDelete(cmd)
	if !ok {
		return nil
	}
	read := value.(*os.File)
	defer func() { _ = read.Close() }()
	_ = cmd.ExtraFiles[len(cmd.ExtraFiles)-1].Close()
	var result supervisorLaunchResult
	if err := json.NewDecoder(read).Decode(&result); err != nil {
		return fmt.Errorf("command supervisor startup: %w", err)
	}
	if result.Errno != 0 {
		return &os.PathError{Op: "fork/exec", Path: cmd.Args[4], Err: syscall.Errno(result.Errno)}
	}
	return nil
}

func init() {
	if len(os.Args) > 1 && os.Args[1] == supervisorCommand {
		os.Exit(runCommandSupervisor(os.Args[2:]))
	}
}

// superviseCommand preserves the target's descriptors and adds an engine-lifetime pipe.
func superviseCommand(cmd *exec.Cmd, cleanup func(), priority ProcessPriority) (*exec.Cmd, func(), error) {
	self, err := os.Executable()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	read, write, err := os.Pipe()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		_ = read.Close()
		_ = write.Close()
		cleanup()
		return nil, nil, err
	}
	count := len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, read, statusWrite)
	supervisorStarts.Store(cmd, statusRead)
	cmd.Args = append([]string{self, supervisorCommand, strconv.Itoa(count), string(priority), cmd.Path}, cmd.Args...)
	cmd.Path = self
	return cmd, func() {
		_ = read.Close()
		_ = write.Close()
		_ = statusRead.Close()
		_ = statusWrite.Close()
		supervisorStarts.Delete(cmd)
		cleanup()
	}, nil
}

func commandSupervisor(cmd *exec.Cmd) bool {
	return cmd != nil && len(cmd.Args) > 1 && cmd.Args[1] == supervisorCommand
}

func signalSupervisor(pid int, start int64, hard bool) bool {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return false
	}
	defer func() { _ = unix.Close(fd) }()
	observed, alive := osprocess.StartTime(pid)
	if !alive || observed != start {
		return false
	}
	sig := unix.SIGTERM
	if hard {
		sig = unix.SIGUSR2
	}
	return unix.PidfdSendSignal(fd, sig, nil, 0) == nil
}

// supervisorTarget is the command the parent asked the supervisor to start.
type supervisorTarget struct {
	extraFiles int
	priority   ProcessPriority
	path       string
	args       []string
}

func parseSupervisorTarget(args []string) (supervisorTarget, bool) {
	if len(args) < 4 {
		return supervisorTarget{}, false
	}
	count, err := strconv.Atoi(args[0])
	if err != nil || count < 0 {
		return supervisorTarget{}, false
	}
	return supervisorTarget{extraFiles: count, priority: ProcessPriority(args[1]), path: args[2], args: args[3:]}, true
}

// command hands the target the descriptors its parent installed for it: the
// standard streams at 0-2, then its extra files from 3.
func (t supervisorTarget) command() *exec.Cmd {
	cmd := &exec.Cmd{Path: t.path, Args: t.args, Env: os.Environ()}
	cmd.Stdin = os.NewFile(0, "target-stdin")
	cmd.Stdout = os.NewFile(1, "target-stdout")
	cmd.Stderr = os.NewFile(2, "target-stderr")
	for i := 0; i < t.extraFiles; i++ {
		cmd.ExtraFiles = append(cmd.ExtraFiles, os.NewFile(uintptr(3+i), "target-descriptor"))
	}
	return cmd
}

// launchFailure reports the errno the parent's fork/exec would have seen.
func launchFailure(err error) supervisorLaunchResult {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return supervisorLaunchResult{Errno: int(errno)}
	}
	return supervisorLaunchResult{Errno: int(syscall.EIO)}
}

// reportLaunch answers supervisorStartupError with the target's launch errno.
func reportLaunch(status io.WriteCloser, started error) error {
	launch := supervisorLaunchResult{}
	if started != nil {
		launch = launchFailure(started)
	}
	err := json.NewEncoder(status).Encode(launch)
	return errors.Join(err, status.Close())
}

// awaitEngineLoss closes the returned channel once the engine's end of the
// lifetime pipe closes, which also happens when the engine dies.
func awaitEngineLoss(monitor *os.File) <-chan struct{} {
	gone := make(chan struct{})
	go func() {
		var b [1]byte
		_, _ = monitor.Read(b[:])
		_ = monitor.Close()
		close(gone)
	}()
	return gone
}

// runCommandSupervisor retains orphaned descendants inside one command's lineage.
func runCommandSupervisor(args []string) int {
	target, ok := parseSupervisorTarget(args)
	if !ok {
		return 125
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return 125
	}
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return 125
	}
	_ = unix.Close(fd)
	events := make(chan os.Signal, 4)
	signal.Notify(events, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR2)
	defer signal.Stop(events)
	gone := awaitEngineLoss(os.NewFile(uintptr(3+target.extraFiles), "engine-lifetime"))
	cmd := target.command()
	started := startSupervisorTarget(cmd, target.priority)
	reported := reportLaunch(os.NewFile(uintptr(4+target.extraFiles), "command-startup"), started)
	if started != nil {
		return 127
	}
	if reported != nil {
		// The parent fails a launch it cannot confirm; stop the primary so the tree settles.
		_ = cmd.Process.Kill()
	}
	for _, file := range cmd.ExtraFiles {
		_ = file.Close()
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	code, sig := supervisorExit(supervision{
		root: os.Getpid(), finished: finished, events: events, gone: gone,
		grace: TerminateGrace, reap: reapAdoptedChildren,
	}.settle())
	if sig != 0 {
		exitWithSignal(sig)
	}
	return code
}

// supervisorExit maps the primary's wait result to the supervisor's exit code,
// and to the signal to re-raise when one killed the primary.
func supervisorExit(result error) (int, syscall.Signal) {
	if result == nil {
		return 0, 0
	}
	var exit *exec.ExitError
	if !errors.As(result, &exit) {
		return 125, 0
	}
	if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal()), status.Signal()
	}
	return exit.ExitCode(), 0
}

// exitWithSignal preserves the primary child's wait status after tree settlement.
func exitWithSignal(sig syscall.Signal) {
	runtime.LockOSThread()
	action := struct {
		handler, flags, restorer uintptr
		mask                     uint64
	}{}
	// Go exposes no SIG_DFL reset, and its own handler would exit 2 or print a
	// traceback for signals such as SIGQUIT instead of dying by the signal.
	_, _, _ = unix.RawSyscall6(unix.SYS_RT_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&action)), 0, 8, 0, 0) //nolint:gosec // G103 — zeroed kernel sigaction (SIG_DFL), 8-byte sigset
	mask := uint64(1) << (uint(sig) - 1)
	_, _, _ = unix.RawSyscall6(unix.SYS_RT_SIGPROCMASK, unix.SIG_UNBLOCK, uintptr(unsafe.Pointer(&mask)), 0, 8, 0, 0) //nolint:gosec // G103 — 8-byte kernel sigset for one signal
	_ = syscall.Kill(os.Getpid(), sig)
}

// Forking from the configured thread gives the primary its requested priority.
func startSupervisorTarget(cmd *exec.Cmd, priority ProcessPriority) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if priority == ProcessPriorityBelowNormal {
		tid := unix.Gettid()
		nice, err := processNice(tid)
		if err != nil {
			return err
		}
		if nice < DefaultBelowNormalNice {
			if err := syscall.Setpriority(syscall.PRIO_PROCESS, tid, DefaultBelowNormalNice); err != nil {
				return err
			}
		}
	}
	return cmd.Start()
}
