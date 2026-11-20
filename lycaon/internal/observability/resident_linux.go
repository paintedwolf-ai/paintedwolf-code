package observability

import "syscall"

// PeakResidentBytes is the process's resident-memory high-water mark.
func PeakResidentBytes() int64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return -1
	}
	return usage.Maxrss * 1024
}
