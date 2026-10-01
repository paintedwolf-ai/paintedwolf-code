//go:build (darwin && !cgo) || dragonfly || freebsd || netbsd || openbsd

package watchfd

import "syscall"

// fallbackDescriptors applies only when the process cannot read its own
// descriptor limit. Guessing high is what exhausts descriptors for real work.
const fallbackDescriptors = 1024

// maxDescriptors caps the pool however generous the host's limit is.
const maxDescriptors = 16384

// Recursive is false: kqueue observes one directory per registration, so a
// tree is covered only as far as the budget registered it.
const Recursive = false

// EntryDescriptors reports whether registrations charge for directory children.
const EntryDescriptors = true

// Cost reports the descriptors fsnotify spends registering one directory:
// kqueue watches descriptors, so fsnotify opens the directory and every entry
// in it. The directory is also counted in its parent's entry list, so the
// total overcounts by one per directory.
func Cost(entries int) int { return entries + 1 }

// poolDescriptors is the watcher's share of the process descriptor limit. A
// sixteenth: the watcher only makes a cache fresher than its TTL, while the
// store, the HTTP server, and every scanner need descriptors for asked-for work.
func poolDescriptors() int {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return fallbackDescriptors
	}
	share := lim.Cur / 16
	if share > uint64(maxDescriptors) {
		return maxDescriptors
	}
	return int(share)
}
