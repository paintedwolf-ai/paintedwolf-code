//go:build linux

package osprocess

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func matchStoppedExecutable(pid int, expected *os.File) (bool, error) {
	root := filepath.Join("/proc", strconv.Itoa(pid))
	if err := requireStopped(root); err != nil {
		return false, err
	}
	observed, err := os.Stat(filepath.Join(root, "exe"))
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrExecutableObservationUnavailable, err)
	}
	held, err := expected.Stat()
	if err != nil {
		return false, err
	}
	if err := requireStopped(root); err != nil {
		return false, err
	}
	return os.SameFile(observed, held), nil
}

func requireStopped(root string) error {
	status, err := os.Open(filepath.Join(root, "status"))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrExecutableObservationUnavailable, err)
	}
	defer status.Close()
	scanner := bufio.NewScanner(io.LimitReader(status, 64<<10))
	stopped := false
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "State:") {
			fields := strings.Fields(strings.TrimPrefix(scanner.Text(), "State:"))
			stopped = len(fields) > 0 && (fields[0] == "T" || fields[0] == "t")
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrExecutableObservationUnavailable, err)
	}
	if !stopped {
		return ErrExecutableObservationUnavailable
	}
	return nil
}
