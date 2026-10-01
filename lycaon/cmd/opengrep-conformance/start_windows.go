package main

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func resumeEvaluationProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(thread)
		previous, err := windows.ResumeThread(thread)
		if err != nil {
			return err
		}
		if previous != 1 {
			return fmt.Errorf("scanner primary thread had unexpected suspend count %d", previous)
		}
		return nil
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	return fmt.Errorf("scanner primary thread unavailable")
}
