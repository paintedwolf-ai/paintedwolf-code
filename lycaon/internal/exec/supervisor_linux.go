//go:build linux

package exec

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"
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
	defer read.Close()
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
	defer unix.Close(fd)
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

// runCommandSupervisor retains orphaned descendants inside one command's lineage.
func runCommandSupervisor(args []string) int {
	if len(args) < 4 {
		return 125
	}
	count, err := strconv.Atoi(args[0])
	if err != nil || count < 0 {
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
	monitor := os.NewFile(uintptr(3+count), "engine-lifetime")
	if monitor == nil {
		return 125
	}
	defer monitor.Close()
	events := make(chan os.Signal, 4)
	signal.Notify(events, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR2)
	defer signal.Stop(events)
	gone := make(chan struct{})
	go func() { var b [1]byte; _, _ = monitor.Read(b[:]); close(gone) }()
	status := os.NewFile(uintptr(4+count), "command-startup")
	defer status.Close()
	cmd := &exec.Cmd{Path: args[2], Args: args[3:], Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Env: os.Environ()}
	for i := 0; i < count; i++ {
		cmd.ExtraFiles = append(cmd.ExtraFiles, os.NewFile(uintptr(3+i), "target-descriptor"))
	}
	if err := startSupervisorTarget(cmd, ProcessPriority(args[1])); err != nil {
		var pathError *os.PathError
		result := supervisorLaunchResult{Errno: int(syscall.EIO)}
		if errors.As(err, &pathError) {
			err = pathError.Err
		}
		var errno syscall.Errno
		if errors.As(err, &errno) {
			result.Errno = int(errno)
		}
		_ = json.NewEncoder(status).Encode(result)
		return 127
	}
	_ = json.NewEncoder(status).Encode(supervisorLaunchResult{})
	_ = status.Close()
	for _, file := range cmd.ExtraFiles {
		_ = file.Close()
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	var result error
	hard := false
	settled := false
	select {
	case result = <-finished:
		settled = true
	case sig := <-events:
		hard = sig == syscall.SIGUSR2
	case <-gone:
		hard = true
	}
	// Wait owns only the primary child; adopted children are reaped after it settles.
	deadline := time.Now().Add(TerminateGrace)
	for {
		sig := unix.SIGTERM
		if hard || time.Now().After(deadline) {
			sig = unix.SIGKILL
		}
		remaining := signalCommandDescendants(os.Getpid(), sig)
		if !settled {
			select {
			case result = <-finished:
				settled = true
			default:
			}
		}
		if settled {
			reapAdoptedChildren()
			if remaining == 0 && len(commandDescendants(os.Getpid())) == 0 {
				break
			}
		}
		select {
		case sig := <-events:
			if sig == syscall.SIGUSR2 {
				hard = true
			}
		case <-gone:
			hard = true
			gone = nil
		case <-time.After(10 * time.Millisecond):
		}
	}
	if result == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(result, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			exitWithSignal(status.Signal())
			return 128 + int(status.Signal())
		}
		return exit.ExitCode()
	}
	return 125
}

// exitWithSignal preserves the primary child's wait status after tree settlement.
func exitWithSignal(sig syscall.Signal) {
	runtime.LockOSThread()
	action := struct {
		handler, flags, restorer uintptr
		mask                     uint64
	}{}
	_, _, _ = unix.RawSyscall6(unix.SYS_RT_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&action)), 0, 8, 0, 0)
	mask := uint64(1) << (uint(sig) - 1)
	_, _, _ = unix.RawSyscall6(unix.SYS_RT_SIGPROCMASK, unix.SIG_UNBLOCK, uintptr(unsafe.Pointer(&mask)), 0, 8, 0, 0)
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
