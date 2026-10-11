//go:build !darwin && !linux && !windows

package desktoptrash

import (
	"fmt"
	"runtime"
)

func platformMove(path string) (Receipt, error) {
	return Receipt{}, fmt.Errorf("system trash is unavailable on %s", runtime.GOOS)
}
