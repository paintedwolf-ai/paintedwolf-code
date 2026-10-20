//go:build linux

package osprocess

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func processFile(pid int, name string, maximum int) ([]byte, error) {
	file, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), name))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExecutableObservationUnavailable, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExecutableObservationUnavailable, err)
	}
	if len(data) > maximum {
		return nil, ErrExecutableObservationUnavailable
	}
	return data, nil
}
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
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok {
			fields[key] = strings.TrimSpace(value)
		}
	}
	var ids [6]uint32
	for index, key := range []string{"Uid", "Gid"} {
		values := strings.Fields(fields[key])
		if len(values) != 4 {
			return ProcessSecurity{}, ErrExecutableObservationUnavailable
		}
		for i := 0; i < 3; i++ {
			value, err := strconv.ParseUint(values[i], 10, 32)
			if err != nil {
				return ProcessSecurity{}, ErrExecutableObservationUnavailable
			}
			ids[3*index+i] = uint32(value)
		}
	}
	var capabilities [3]uint64
	for i, key := range []string{"CapPrm", "CapEff", "CapAmb"} {
		value, err := strconv.ParseUint(fields[key], 16, 64)
		if err != nil {
			return ProcessSecurity{}, ErrExecutableObservationUnavailable
		}
		capabilities[i] = value
	}
	if fields["NoNewPrivs"] != "0" && fields["NoNewPrivs"] != "1" {
		return ProcessSecurity{}, ErrExecutableObservationUnavailable
	}
	return ProcessSecurity{RealUID: ids[0], EffectiveUID: ids[1], SavedUID: ids[2], RealGID: ids[3], EffectiveGID: ids[4], SavedGID: ids[5], Linux: &LinuxPrivileges{Permitted: capabilities[0], Effective: capabilities[1], Ambient: capabilities[2], NoNewPrivileges: fields["NoNewPrivs"] == "1"}}, nil
}
func processArguments(pid int, maximumBytes int) ([]string, []string, error) {
	args, err := processFile(pid, "cmdline", maximumBytes)
	if err != nil {
		return nil, nil, err
	}
	env, err := processFile(pid, "environ", maximumBytes-len(args))
	if err != nil {
		return nil, nil, err
	}
	argumentWords, err := nulWords(args)
	if err != nil {
		return nil, nil, err
	}
	environmentWords, err := nulWords(env)
	if err != nil {
		return nil, nil, err
	}
	return argumentWords, environmentWords, nil
}
func nulWords(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != 0 {
		return nil, ErrExecutableObservationUnavailable
	}
	var words []string
	for len(data) > 0 {
		end := bytes.IndexByte(data, 0)
		if end < 0 {
			return nil, ErrExecutableObservationUnavailable
		}
		words = append(words, string(data[:end]))
		data = data[end+1:]
	}
	return words, nil
}
