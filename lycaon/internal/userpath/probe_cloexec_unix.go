//go:build unix

package userpath

import (
	"os"
	"strconv"
	"syscall"
)

const fallbackCloseOnExecLimit = 1024

// closeOnExecInherited marks non-standard descriptors close-on-exec so a
// login-shell profile or daemon it starts cannot inherit parent locks or pipes.
func closeOnExecInherited() {
	closeOnExecIn("/dev/fd", fallbackCloseOnExecLimit)
}

func closeOnExecIn(fdDir string, fallbackMax int) {
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		for fd := 3; fd < fallbackMax; fd++ {
			syscall.CloseOnExec(fd)
		}
		return
	}
	for _, entry := range entries {
		if fd, err := strconv.Atoi(entry.Name()); err == nil && fd >= 3 {
			syscall.CloseOnExec(fd)
		}
	}
}
