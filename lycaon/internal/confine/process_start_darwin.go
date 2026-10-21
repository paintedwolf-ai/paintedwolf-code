//go:build darwin

package confine

import "golang.org/x/sys/unix"

func nativeArgumentBudget() (int, error) {
	maximum, err := unix.SysctlUint32("kern.argmax")
	return int(maximum), err
}
