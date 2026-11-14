package denialprobe

import "golang.org/x/sys/unix"

func unmount(path string) error { return unix.Unmount(path, 0) }

func mount(path string) error { return unix.Mount("none", path, "tmpfs", 0, "") }
