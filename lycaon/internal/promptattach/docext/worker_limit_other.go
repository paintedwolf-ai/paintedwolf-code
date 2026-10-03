//go:build unix && !linux

package docext

import "golang.org/x/sys/unix"

func applyWorkerMemoryLimit(limit int64) {
	if limit <= 0 {
		return
	}
	value := uint64(limit)
	_ = unix.Setrlimit(unix.RLIMIT_AS, &unix.Rlimit{Cur: value, Max: value})
}
