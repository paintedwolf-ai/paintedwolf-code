//go:build windows

package docext

import (
	"runtime/debug"
	"unsafe"

	"golang.org/x/sys/windows"
)

var extractionWorkerJob windows.Handle

func applyWorkerMemoryLimit(limit int64) {
	if limit > 0 {
		debug.SetMemoryLimit(limit)
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			ProcessMemoryLimit: uintptr(limit),
		}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
		if _, err := windows.SetInformationJobObject(
			job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		); err != nil {
			_ = windows.CloseHandle(job)
			return
		}
		if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
			_ = windows.CloseHandle(job)
			return
		}
		extractionWorkerJob = job
	}
}
