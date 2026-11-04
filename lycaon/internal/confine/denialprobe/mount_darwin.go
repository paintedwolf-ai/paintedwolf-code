package denialprobe

import "golang.org/x/sys/unix"

func unmount(path string) error { return unix.Unmount(path, 0) }

// mount asks for a filesystem an ordinary user may mount. Null arguments answer EFAULT once the
// kernel reaches the filesystem, so only the boundary answers EPERM.
func mount(path string) error { return unix.Mount("nfs", path, 0, nil) }
