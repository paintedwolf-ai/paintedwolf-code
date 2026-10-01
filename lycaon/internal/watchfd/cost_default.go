//go:build !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package watchfd

// Recursive is false: inotify and ReadDirectoryChangesW as fsnotify drives
// them report on one directory per registration.
const Recursive = false

// EntryDescriptors reports whether registrations charge for directory children.
const EntryDescriptors = false

// Cost reports what fsnotify spends registering one directory. inotify and
// ReadDirectoryChangesW watch the directory itself, so a directory costs one
// registration however many entries it holds.
func Cost(int) int { return 1 }

// poolDescriptors matches the directory cap where a watch costs one unit; a
// second bound in the same unit would only restate MaxDirs.
func poolDescriptors() int { return MaxDirs }
