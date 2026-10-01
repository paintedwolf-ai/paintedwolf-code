package main

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type evaluationProcess struct {
	job windows.Handle
	cmd *exec.Cmd
}

func newEvaluationProcess(cmd *exec.Cmd) (*evaluationProcess, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	process := &evaluationProcess{job: job, cmd: cmd}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		process.close()
		return nil, err
	}
	// The scanner must join the job before it can create untracked workers.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED}
	cmd.Cancel = func() error { process.kill(); return nil }
	return process, nil
}

func (p *evaluationProcess) started(pid int) error {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(p.job, handle); err != nil {
		return err
	}
	return resumeEvaluationProcess(uint32(pid))
}
func (p *evaluationProcess) close() { _ = windows.CloseHandle(p.job) }
func (p *evaluationProcess) kill() {
	_ = windows.TerminateJobObject(p.job, 1)
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (p *evaluationProcess) sample() (processMemorySample, error) {
	var members struct {
		Assigned uint32
		Count    uint32
		PIDs     [1024]uintptr
	}
	if err := windows.QueryInformationJobObject(p.job, windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&members)), uint32(unsafe.Sizeof(members)), nil); err != nil {
		return processMemorySample{}, err
	}
	if members.Count > uint32(len(members.PIDs)) {
		return processMemorySample{}, fmt.Errorf("scanner job exceeds process measurement bound")
	}
	var memory processMemorySample
	for _, pid := range members.PIDs[:members.Count] {
		rss, err := windowsResidentBytes(uint32(pid))
		if err != nil {
			return processMemorySample{}, err
		}
		memory.RSSBytes += rss
		memory.Processes++
	}
	return memory, nil
}

var processMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

type processMemoryCounters struct {
	Size, PageFaults               uint32
	PeakWorkingSet, WorkingSet     uintptr
	PeakPagedPool, PagedPool       uintptr
	PeakNonpagedPool, NonpagedPool uintptr
	Pagefile, PeakPagefile         uintptr
}

func windowsResidentBytes(pid uint32) (int64, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(handle)
	var memory processMemoryCounters
	memory.Size = uint32(unsafe.Sizeof(memory))
	ok, _, callErr := processMemoryInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&memory)), uintptr(memory.Size))
	if ok == 0 {
		return 0, callErr
	}
	return int64(memory.WorkingSet), nil
}
