//go:build linux

package fseffect

import "golang.org/x/sys/unix"

func metadataOpenFlags() int { return unix.O_PATH }
