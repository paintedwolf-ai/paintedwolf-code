//go:build !darwin && !linux && !windows

package desktoptrash

import (
	"fmt"
	"runtime"
)

func platformMove(path string) error {
	return fmt.Errorf("system trash is unavailable on %s", runtime.GOOS)
}
