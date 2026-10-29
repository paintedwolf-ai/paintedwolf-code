package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type dedicatedProtection struct {
	uids, gids                                           [4]int
	groups                                               []int
	effective, permitted, inheritable, bounding, ambient uint64
	noNewPrivileges                                      int
}

func requireDedicatedProtection() error {
	facts, err := readDedicatedProtection()
	if err != nil {
		return fmt.Errorf("read dedicated service protection: %w", err)
	}
	return facts.validate()
}

func readDedicatedProtection() (dedicatedProtection, error) {
	// The supervisor sets credentials before exec; new runtime threads inherit
	// them. Pin this thread while capturing its complete credential floor.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var facts dedicatedProtection
	file, err := os.Open("/proc/thread-self/status")
	if err != nil {
		return facts, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	_ = file.Close()
	if err != nil {
		return facts, err
	}
	if len(raw) > 16<<10 {
		return facts, errors.New("kernel credential status exceeds bound")
	}
	facts.uids, facts.gids, err = credentialIDs(raw)
	if err != nil {
		return facts, err
	}
	facts.groups, err = os.Getgroups()
	if err != nil {
		return facts, err
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var caps [2]unix.CapUserData
	if err := unix.Capget(&header, &caps[0]); err != nil {
		return facts, err
	}
	facts.effective = uint64(caps[0].Effective) | uint64(caps[1].Effective)<<32
	facts.permitted = uint64(caps[0].Permitted) | uint64(caps[1].Permitted)<<32
	facts.inheritable = uint64(caps[0].Inheritable) | uint64(caps[1].Inheritable)<<32
	for capability := 0; capability < 64; capability++ {
		bound, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, uintptr(capability), 0, 0, 0)
		if errors.Is(err, unix.EINVAL) {
			break
		}
		if err != nil {
			return facts, err
		}
		ambient, err := unix.PrctlRetInt(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_IS_SET, uintptr(capability), 0, 0)
		if err != nil {
			return facts, err
		}
		if bound != 0 {
			facts.bounding |= uint64(1) << capability
		}
		if ambient != 0 {
			facts.ambient |= uint64(1) << capability
		}
	}
	facts.noNewPrivileges, err = unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	return facts, err
}

func (facts dedicatedProtection) validate() error {
	const ptrace = uint64(1) << unix.CAP_SYS_PTRACE
	// systemd repeats the primary group; it grants no additional group authority.
	extraGroup := slices.ContainsFunc(facts.groups, func(group int) bool { return group != facts.gids[0] })
	unequalIDs := slices.ContainsFunc(facts.uids[:], func(uid int) bool { return uid != facts.uids[0] }) || slices.ContainsFunc(facts.gids[:], func(gid int) bool { return gid != facts.gids[0] })
	if facts.uids[0] == 0 || facts.gids[0] == 0 || unequalIDs || extraGroup || facts.noNewPrivileges != 1 || facts.effective != ptrace || facts.permitted != ptrace || facts.inheritable != ptrace || facts.bounding != ptrace || facts.ambient != ptrace {
		return errors.New("dedicated service requires non-root uid/gid, no group authority beyond its primary group, no-new-privileges and only CAP_SYS_PTRACE in every capability set")
	}
	return nil
}

func credentialIDs(raw []byte) (uids, gids [4]int, err error) {
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (fields[0] != "Uid:" && fields[0] != "Gid:") {
			continue
		}
		if seen[fields[0]] || len(fields) != 5 {
			return uids, gids, errors.New("malformed or repeated kernel credential field")
		}
		seen[fields[0]] = true
		var ids [4]int
		for index, field := range fields[1:] {
			value, parseErr := strconv.ParseUint(field, 10, 32)
			if parseErr != nil {
				return uids, gids, parseErr
			}
			ids[index] = int(value)
		}
		if fields[0] == "Uid:" {
			uids = ids
		} else {
			gids = ids
		}
	}
	if !seen["Uid:"] || !seen["Gid:"] {
		return uids, gids, errors.New("kernel credential fields missing")
	}
	return uids, gids, nil
}
