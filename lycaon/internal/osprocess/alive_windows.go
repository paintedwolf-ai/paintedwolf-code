//go:build windows

package osprocess

import "golang.org/x/sys/windows"

// Alive reports whether pid names a running process. A handle can outlive its
// process, so exit shows in the wait state, not in whether the handle opens.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	event, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && event == uint32(windows.WAIT_TIMEOUT)
}
