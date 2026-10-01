//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

func execLogsSibling(path string, args []string) error {
	argv := append([]string{path}, args...)
	//nolint:gosec // G204 — path is the staged sibling beside this binary
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", path, err)
	}
	return nil
}
