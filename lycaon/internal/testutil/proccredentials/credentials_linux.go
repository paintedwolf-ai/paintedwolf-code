//go:build linux

// Package proccredentials reads native credentials for process qualification fixtures.
package proccredentials

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
)

var errIncompleteStatus = errors.New("incomplete native process credentials")

// ProcessSecurity records native IDs and Linux privileges.
type ProcessSecurity struct {
	RealUID, EffectiveUID, SavedUID uint32
	RealGID, EffectiveGID, SavedGID uint32
	Linux                           *LinuxPrivileges
}

// LinuxPrivileges distinguishes observed capability sets and no-new-privileges.
type LinuxPrivileges struct {
	Permitted, Effective, Ambient uint64
	Inheritable, Bounding         uint64
	NoNewPrivileges               bool
}

// ReadSelf returns the credentials recorded by the kernel for the current process.
func ReadSelf() (ProcessSecurity, error) {
	const maxStatusBytes = 64 << 10
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return ProcessSecurity{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxStatusBytes+1))
	if err != nil {
		return ProcessSecurity{}, err
	}
	if len(data) > maxStatusBytes {
		return ProcessSecurity{}, errIncompleteStatus
	}
	return parseProcessSecurity(data)
}

func parseProcessSecurity(data []byte) (ProcessSecurity, error) {
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
			return ProcessSecurity{}, errIncompleteStatus
		}
		for i := 0; i < 3; i++ {
			value, err := strconv.ParseUint(values[i], 10, 32)
			if err != nil {
				return ProcessSecurity{}, errIncompleteStatus
			}
			ids[3*index+i] = uint32(value)
		}
	}
	var capabilities [5]uint64
	for i, key := range []string{"CapPrm", "CapEff", "CapAmb", "CapInh", "CapBnd"} {
		value, err := strconv.ParseUint(fields[key], 16, 64)
		if err != nil {
			return ProcessSecurity{}, errIncompleteStatus
		}
		capabilities[i] = value
	}
	if fields["NoNewPrivs"] != "0" && fields["NoNewPrivs"] != "1" {
		return ProcessSecurity{}, errIncompleteStatus
	}
	return ProcessSecurity{RealUID: ids[0], EffectiveUID: ids[1], SavedUID: ids[2], RealGID: ids[3], EffectiveGID: ids[4], SavedGID: ids[5], Linux: &LinuxPrivileges{Permitted: capabilities[0], Effective: capabilities[1], Ambient: capabilities[2], Inheritable: capabilities[3], Bounding: capabilities[4], NoNewPrivileges: fields["NoNewPrivs"] == "1"}}, nil
}
