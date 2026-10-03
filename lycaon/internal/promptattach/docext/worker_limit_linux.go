package docext

import "golang.org/x/sys/unix"

// applyWorkerMemoryLimit caps the memory the worker writes to. Linux counts
// reserved address space against RLIMIT_AS, including the executable's own
// mapping and the runtime's arena reservations, so that limit fails a worker
// before it parses anything; RLIMIT_DATA counts private writable memory as it
// is committed, like the Windows job limit.
func applyWorkerMemoryLimit(limit int64) {
	if limit <= 0 {
		return
	}
	value := uint64(limit)
	_ = unix.Setrlimit(unix.RLIMIT_DATA, &unix.Rlimit{Cur: value, Max: value})
}
