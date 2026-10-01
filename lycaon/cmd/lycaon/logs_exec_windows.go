//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
)

func execLogsSibling(path string, args []string) error {
	//nolint:gosec // G204 — path is the staged sibling beside this binary
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s: %w", path, err)
	}
	return nil
}
