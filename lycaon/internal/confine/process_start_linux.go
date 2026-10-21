//go:build linux

package confine

import (
	"golang.org/x/sys/unix"
	"os"
)

func nativeArgumentBudget() (int, error) {
	var stack unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_STACK, &stack); err != nil {
		return 0, err
	}
	return linuxArgumentBudget(stack.Cur, os.Getpagesize()), nil
}
func linuxArgumentBudget(stack uint64, pageBytes int) int {
	return max(32*pageBytes, int(min(stack/4, uint64(6<<20))))
}
