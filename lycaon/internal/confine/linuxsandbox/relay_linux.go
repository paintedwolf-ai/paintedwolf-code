//go:build linux

package linuxsandbox

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// signalReportFD is where the inner helper finds its report pipe to the
// outer helper. The plan is on 3 and the marker on 4.
const signalReportFD = 5

// reportReady is the report byte that says the inner helper has its signal
// handlers; any other byte names the signal that killed the target.
const reportReady = 0xff

// Relaunch runs the helper again as PID 1 of fresh user, mount, PID, and
// network namespaces, hands it the plan over an inherited pipe and the lineage
// marker when there is one, and relays signals and exit status until it ends.
// The returned signal, when set, killed the target and should end this
// process the same way.
func Relaunch(self string, args []string, plan []byte, marker *os.File) (code int, sig syscall.Signal, err error) {
	// The parent-death signal is tied to the thread that forks.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	read, write, err := os.Pipe()
	if err != nil {
		return 0, 0, fmt.Errorf("plan pipe: %w", err)
	}
	report, reportWrite, err := os.Pipe()
	if err != nil {
		_ = read.Close()
		_ = write.Close()
		return 0, 0, fmt.Errorf("signal report pipe: %w", err)
	}
	defer func() { _ = report.Close() }()
	cmd := exec.Command(self, args...)
	// The standard descriptors are the command's streams, handed through as is.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.NewFile(0, "stdin"), os.NewFile(1, "stdout"), os.NewFile(2, "stderr")
	// A nil marker leaves its descriptor closed in the child.
	cmd.ExtraFiles = []*os.File{read, marker, reportWrite}
	cmd.SysProcAttr = namespaceAttr()
	events := notifyForwarded()
	defer signal.Stop(events)
	err = cmd.Start()
	_ = read.Close()
	_ = reportWrite.Close()
	if err != nil {
		_ = write.Close()
		return 0, 0, classifyLaunch(err)
	}
	if marker != nil {
		_ = marker.Close()
	}
	go func() {
		defer func() { _ = write.Close() }()
		_, _ = io.Copy(write, bytes.NewReader(plan))
	}()
	ready := make(chan struct{})
	reported := make(chan syscall.Signal, 1)
	go func() { reported <- readReport(report, ready) }()
	code, sig, held := relay(cmd.Process.Pid, cmd.Wait, events, ready)
	switch r := <-reported; {
	case r != 0:
		sig = r
	case len(held) > 0:
		// The inner helper ended before it could take the signal it was sent.
		sig = held[0]
	}
	return code, sig, nil
}

// readReport reads the inner helper's report pipe until it closes, closing
// ready when the helper says its handlers are in place, and returns the
// signal it named, if any.
func readReport(report io.Reader, ready chan<- struct{}) syscall.Signal {
	var sig syscall.Signal
	readyClosed := false
	buf := make([]byte, 8)
	for {
		n, err := report.Read(buf)
		for _, b := range buf[:n] {
			switch {
			case b == reportReady && !readyClosed:
				close(ready)
				readyClosed = true
			case b != 0 && b < 65:
				sig = syscall.Signal(b)
			}
		}
		if err != nil {
			return sig
		}
	}
}

// heldSignals receives the forwarded signals in the inner helper from before
// the target exists, so none is lost or handled by the runtime meanwhile.
var heldSignals chan os.Signal

// HoldSignals takes the forwarded signals in the inner helper and tells the
// outer helper it may now forward them. Until then the outer helper keeps
// them: Go's own handler would end PID 1 with exit 2, not by the signal.
func HoldSignals() {
	heldSignals = notifyForwarded()
	if f := reportPipe(); f != nil {
		_, _ = f.Write([]byte{reportReady})
	}
}

// ReportSignal names the signal that killed the target to the outer helper.
// The inner helper is PID 1 of its PID namespace, which the kernel never lets
// die by a signal it sends itself, so the outer helper dies by it instead.
func ReportSignal(sig syscall.Signal) {
	if f := reportPipe(); f != nil {
		_, _ = f.Write([]byte{byte(sig)})
	}
}

// reportPipe is the inherited report pipe, or nil when this helper was not
// started by Relaunch and the descriptor is something else or nothing.
func reportPipe() *os.File {
	var st unix.Stat_t
	if unix.Fstat(signalReportFD, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFIFO {
		return nil
	}
	return os.NewFile(signalReportFD, "signal-report")
}

// namespaceAttr creates the namespaces, maps this one uid and gid onto
// themselves, keeps the capabilities the inner helper needs to build the
// boundary, and ends the inner helper if this process dies.
func namespaceAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Cloneflags:                 unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWNET,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
		AmbientCaps:                []uintptr{unix.CAP_SYS_ADMIN, unix.CAP_NET_ADMIN, unix.CAP_SETPCAP},
		Pdeathsig:                  syscall.SIGKILL,
	}
}

// Supervise runs the target as this process's child and stays as the
// sandbox's reaper: it forwards the host's signals, collects every process the
// target leaves, and ends with the target's status. afterStart runs once the
// target exists. The returned error is the raw exec failure, for the caller to
// classify.
func Supervise(path string, argv, env []string, afterStart func()) (code int, sig syscall.Signal, err error) {
	// The target must not inherit the outer helper's signal report.
	syscall.CloseOnExec(signalReportFD)
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return 0, 0, fmt.Errorf("child subreaper: %w", err)
	}
	pid, err := syscall.ForkExec(path, argv, &syscall.ProcAttr{Env: env, Files: []uintptr{0, 1, 2}})
	if err != nil {
		return 0, 0, err
	}
	if afterStart != nil {
		afterStart()
	}
	events := heldSignals
	if events == nil {
		events = notifyForwarded()
	}
	defer signal.Stop(events)
	code, sig, _ = relay(pid, func() error { return reapUntil(pid) }, events, nil)
	return code, sig, nil
}

// reapUntil collects every exited child until the target itself is collected,
// and returns its status as os/exec would report it.
func reapUntil(target int) error {
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, 0, nil)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return fmt.Errorf("wait for target: %w", err)
		}
		if pid == target {
			if status.Exited() && status.ExitStatus() == 0 {
				return nil
			}
			return &exitStatusError{status: status}
		}
	}
}

// exitStatusError carries a wait status the way exec.ExitError does.
type exitStatusError struct{ status syscall.WaitStatus }

func (e *exitStatusError) Error() string { return "target exited" }
func (e *exitStatusError) Sys() any      { return e.status }

func notifyForwarded() chan os.Signal {
	events := make(chan os.Signal, 8)
	signal.Notify(events, forwardedSignals()...)
	return events
}

// relay forwards events to the child until wait returns, then maps the
// child's status to an exit code and the signal that killed it. With a ready
// channel, signals are held until it closes; held returns those never sent.
func relay(child int, wait func() error, events <-chan os.Signal, ready <-chan struct{}) (code int, sig syscall.Signal, held []syscall.Signal) {
	finished := make(chan error, 1)
	go func() { finished <- wait() }()
	if ready == nil {
		open := make(chan struct{})
		close(open)
		ready = open
	}
	isReady := false
	for {
		select {
		case <-ready:
			ready, isReady = nil, true
			for _, s := range held {
				_ = syscall.Kill(child, s)
			}
			held = nil
		case ev := <-events:
			s, ok := ev.(syscall.Signal)
			switch {
			case !ok:
			case isReady:
				_ = syscall.Kill(child, s)
			default:
				held = append(held, s)
			}
		case err := <-finished:
			code, sig = exitOf(err)
			return code, sig, held
		}
	}
}

// forwardedSignals are the signals the host may send the helper for the
// target. A terminal delivers its own signals to the whole foreground group,
// so those are not forwarded again when the helper is attached to one.
func forwardedSignals() []os.Signal {
	signals := []os.Signal{syscall.SIGTERM, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGCONT, syscall.SIGALRM}
	if !stdinIsTerminal() {
		signals = append(signals, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP)
	}
	return signals
}

func stdinIsTerminal() bool {
	_, err := unix.IoctlGetTermios(0, unix.TCGETS)
	return err == nil
}

// exitOf maps a wait result to the exit code and killing signal to mirror.
func exitOf(err error) (int, syscall.Signal) {
	if err == nil {
		return 0, 0
	}
	var sys interface{ Sys() any }
	if !errors.As(err, &sys) {
		return 125, 0
	}
	status, ok := sys.Sys().(syscall.WaitStatus)
	if !ok {
		return 125, 0
	}
	if status.Signaled() {
		return 128 + int(status.Signal()), status.Signal()
	}
	return status.ExitStatus(), 0
}

// ExitWithSignal ends this process by the signal that ended the child, so the
// host sees the same wait status. It falls back to 128 plus the signal number.
func ExitWithSignal(sig syscall.Signal) {
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
	// Aimed at this thread, the signal lands before the syscall returns; a
	// process-wide kill would race the exit below.
	_ = unix.Tgkill(os.Getpid(), unix.Gettid(), sig)
	os.Exit(128 + int(sig))
}
