//go:build windows

package exec

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsGuard struct {
	job         windows.Handle
	belowNormal bool
}

func newRunGuard(priority ProcessPriority) (runGuard, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configure job object: %w", err)
	}
	return &windowsGuard{
		job:         job,
		belowNormal: priority == ProcessPriorityBelowNormal,
	}, nil
}

func (g *windowsGuard) configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP)
	if g != nil && g.belowNormal {
		flags |= windows.BELOW_NORMAL_PRIORITY_CLASS
	}
	cmd.SysProcAttr.CreationFlags = flags
	// Cancellation terminates the entire process job.
	cmd.Cancel = func() error {
		g.terminate(cmd)
		return nil
	}
}

// terminate has no graceful signal on Windows; the job ends at once.
func (g *windowsGuard) terminate(cmd *exec.Cmd) {
	g.kill(cmd)
}

func (g *windowsGuard) onStarted(cmd *exec.Cmd) error {
	if g == nil || g.job == 0 || cmd == nil || cmd.Process == nil {
		return nil
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("open process for job assign: %w", err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(g.job, handle); err != nil {
		return fmt.Errorf("assign process to job: %w", err)
	}
	return nil
}

func (g *windowsGuard) kill(cmd *exec.Cmd) {
	if g != nil && g.job != 0 {
		_ = windows.TerminateJobObject(g.job, 1)
		return
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (g *windowsGuard) release() {
	if g != nil && g.job != 0 {
		_ = windows.CloseHandle(g.job)
		g.job = 0
	}
}
