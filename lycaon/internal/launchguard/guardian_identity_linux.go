//go:build linux

package launchguard

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Authenticate the readonly transition proof after the namespace descriptors
// have closed. Full maps preserve kernel identities; they grant no initial
// namespace capability to the guardian.
func authenticateGuardianTransition(file *os.File, spec EntrySpec) error {
	raw, err := readGuardianSealed(file, guardianTransitionHeader+65536*4)
	if err != nil {
		return err
	}
	if len(raw) < guardianTransitionHeader || string(raw[:8]) != "PWGUARD1" || binary.LittleEndian.Uint32(raw[8:]) != 1 || binary.LittleEndian.Uint32(raw[12:]) != guardianTransitionRoles {
		return errors.New("invalid guardian transition proof")
	}
	credentials := raw[16+guardianTransitionRoles*24:]
	count := binary.LittleEndian.Uint32(credentials[8:])
	if count > 65536 || len(credentials) != 16+int(count)*4 || binary.LittleEndian.Uint32(credentials[12:]) != 0 || binary.LittleEndian.Uint32(credentials) != spec.UID || binary.LittleEndian.Uint32(credentials[4:]) != spec.GID {
		return errors.New("guardian proof differs from caller credentials")
	}
	groups := make([]uint32, count)
	for index := range groups {
		groups[index] = binary.LittleEndian.Uint32(credentials[16+4*index:])
	}
	if !slices.Equal(groups, spec.Groups) {
		return errors.New("guardian proof differs from caller groups")
	}
	current, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	kind, err := unix.IoctlRetInt(int(current.Fd()), unix.NS_GET_NSTYPE)
	if err != nil || kind != unix.CLONE_NEWUSER {
		return errors.New("guardian lacks a user namespace")
	}
	owner, err := unix.IoctlGetInt(int(current.Fd()), unix.NS_GET_OWNER_UID)
	if err != nil || owner != 0 {
		return errors.New("guardian namespace is not initial-root owned")
	}
	for _, name := range []string{"uid_map", "gid_map"} {
		data, err := os.ReadFile("/proc/self/" + name)
		if err != nil || !fullIdentityMap(string(data)) {
			return errors.New("guardian namespace lacks full identity maps")
		}
	}
	for index := 0; index < guardianTransitionRoles; index++ {
		fd := guardianRoleFD(index)
		if fd < 0 || fd > math.MaxUint32 {
			return errors.New("guardian role exceeds transition width")
		}
		row := raw[16+24*index:]
		if binary.LittleEndian.Uint32(row) != uint32(fd) || binary.LittleEndian.Uint32(row[4:]) != 0 {
			return errors.New("guardian proof has invalid role ordering")
		}
		if fd == 4 || fd == 8 || fd == 9 {
			continue
		} // Closed source, exec image and initial-userns handles.
		actualFD := fd
		if fd == 3 {
			actualFD = int(current.Fd())
		}
		var actual unix.Statx_t
		if err := unix.Statx(actualFD, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS, &actual); err != nil {
			return err
		}
		if actual.Mask&unix.STATX_INO == 0 || actual.Ino != binary.LittleEndian.Uint64(row[16:]) || uint64(actual.Dev_major)<<32|uint64(actual.Dev_minor) != binary.LittleEndian.Uint64(row[8:]) {
			return errors.New("guardian descriptor differs from privileged selection")
		}
		if fd == 3 && actual.Ino == uint64(0xEFFFFFFD) {
			return errors.New("guardian retained initial-userns authority")
		}
	}
	return nil
}

func sealGuardianIdentity(spec EntrySpec) error {
	if err := sameIdentity(spec); err != nil {
		return err
	}
	if err := prctlAllThreads(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if err := clearCapabilities(); err != nil {
		return err
	}
	for capability := uintptr(0); capability < 64; capability++ {
		value, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, capability, 0, 0, 0)
		if errors.Is(err, unix.EINVAL) {
			break
		}
		if err != nil || value != 0 {
			return errors.New("guardian retained a bounding capability")
		}
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	if value, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0); err != nil || value != 0 {
		return errors.New("guardian remained dumpable")
	}
	return nil
}

func verifyGuardianPIDFD(fd int, expected int) error {
	raw, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(fd))
	if err != nil {
		return err
	}
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || key != "Pid" {
			continue
		}
		fields := strings.Fields(value)
		if found || len(fields) != 1 {
			return errors.New("invalid registered pidfd metadata")
		}
		found = true
		pid, err := strconv.ParseInt(fields[0], 10, 32)
		if err != nil || (int(pid) != expected && pid != -1) {
			return errors.New("registered pidfd differs from selected protected role")
		}
		if pid == -1 {
			pollFD, err := guardianPollDescriptor(uintptr(fd))
			if err != nil {
				return err
			}
			polls := []unix.PollFd{{Fd: pollFD, Events: unix.POLLIN}}
			if _, err := unix.Poll(polls, 0); err != nil || polls[0].Revents&unix.POLLIN == 0 || polls[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
				return errors.New("unbound registered pidfd is not exited")
			}
		}
	}
	if !found {
		return errors.New("registered role is not a pidfd")
	}
	return nil
}
