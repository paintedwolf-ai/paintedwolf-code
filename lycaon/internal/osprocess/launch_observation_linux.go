//go:build linux

package osprocess

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func matchProcessDirectory(pid int, expected *os.File) (bool, error) {
	if expected == nil {
		return false, ErrExecutableObservationUnavailable
	}
	current, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid), "cwd"))
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrExecutableObservationUnavailable, err)
	}
	held, err := expected.Stat()
	if err != nil {
		return false, err
	}
	return os.SameFile(current, held), nil
}
func processSecurity(pid int) (ProcessSecurity, error) {
	data, err := processFile(pid, "status", 64<<10)
	if err != nil {
		return ProcessSecurity{}, err
	}
	return parseProcessSecurity(data)
}

func processArguments(pid int, maximumBytes int) ([]string, []string, bool, error) {
	args, err := processFile(pid, "cmdline", maximumBytes)
	if err != nil {
		return nil, nil, false, err
	}
	env, err := processFile(pid, "environ", maximumBytes-len(args))
	if err != nil {
		return nil, nil, false, err
	}
	argumentWords, err := nulWords(args)
	if err != nil {
		return nil, nil, false, err
	}
	environmentWords, err := nulWords(env)
	if err != nil {
		return nil, nil, false, err
	}
	return argumentWords, environmentWords, true, nil
}
