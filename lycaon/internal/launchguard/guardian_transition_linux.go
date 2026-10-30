//go:build linux

package launchguard

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

const guardianTransitionRoles = 16
const guardianTransitionHeader = 16 + guardianTransitionRoles*24 + 16

// The static transition consumes only these descriptor identities and caller
// credentials. It accepts no paths, executable selectors or namespace labels.
func sealGuardianTransition(roles [guardianTransitionRoles]*os.File, caller Identity) (_ *os.File, err error) {
	if err := initialUserNamespace(); err != nil {
		return nil, err
	}
	if os.Getuid() != 0 || os.Geteuid() != 0 || caller.UID == 0 || caller.UID == ^uint32(0) || caller.GID == ^uint32(0) {
		return nil, errors.New("invalid guardian transition identity")
	}
	groupCount := len(caller.Groups)
	if groupCount > 65536 {
		return nil, errors.New("invalid guardian transition identity")
	}
	raw := make([]byte, guardianTransitionHeader+4*groupCount)
	copy(raw, "PWGUARD1")
	binary.LittleEndian.PutUint32(raw[8:], 1)
	binary.LittleEndian.PutUint32(raw[12:], guardianTransitionRoles)
	for index, file := range roles {
		if file == nil {
			return nil, errors.New("guardian transition lacks a fixed role")
		}
		var stat unix.Statx_t
		if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS, &stat); err != nil {
			return nil, err
		}
		if stat.Mask&unix.STATX_INO == 0 || stat.Ino == 0 {
			return nil, errors.New("guardian role lacks kernel identity")
		}
		fd := guardianRoleFD(index)
		if fd < 0 || fd > math.MaxUint32 {
			return nil, errors.New("guardian role exceeds transition width")
		}
		row := raw[16+index*24:]
		binary.LittleEndian.PutUint32(row, uint32(fd))
		binary.LittleEndian.PutUint64(row[8:], uint64(stat.Dev_major)<<32|uint64(stat.Dev_minor))
		binary.LittleEndian.PutUint64(row[16:], stat.Ino)
	}
	credentials := raw[16+guardianTransitionRoles*24:]
	binary.LittleEndian.PutUint32(credentials, caller.UID)
	binary.LittleEndian.PutUint32(credentials[4:], caller.GID)
	binary.LittleEndian.PutUint32(credentials[8:], uint32(groupCount))
	for index, group := range caller.Groups {
		if group == ^uint32(0) {
			return nil, errors.New("invalid guardian supplementary group")
		}
		binary.LittleEndian.PutUint32(credentials[16+index*4:], group)
	}
	fd, err := unix.MemfdCreate("painted-wolf-guardian-transition", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "sealed-guardian-transition")
	defer func() {
		if err != nil {
			_ = file.Close()
		}
	}()
	if _, err = file.Write(raw); err != nil {
		return nil, err
	}
	if err := file.Chmod(0400); err != nil {
		return nil, err
	}
	_, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, entrySeals)
	return file, err
}

// Keep the root-created namespace object after its opener has exited. Its
// immutable owner/parent and full identity maps protect the caller guardian
// across exec even on hosts where exec resets process dumpability.
func (opener *mountSourceOpener) guardianNamespace(origin *os.File) (_ *os.File, err error) {
	file, err := os.Open("/proc/" + strconv.Itoa(opener.process.Process.Pid) + "/ns/user")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = file.Close()
		}
	}()
	kind, err := unix.IoctlRetInt(int(file.Fd()), unix.NS_GET_NSTYPE)
	if err != nil || kind != unix.CLONE_NEWUSER {
		return nil, errors.New("guardian namespace is not a user namespace")
	}
	owner, err := unix.IoctlGetInt(int(file.Fd()), unix.NS_GET_OWNER_UID)
	if err != nil || owner != 0 {
		return nil, errors.New("guardian namespace lacks initial-root ownership")
	}
	parentFD, err := unix.IoctlRetInt(int(file.Fd()), unix.NS_GET_PARENT)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(parentFD), "guardian-initial-parent")
	defer func() { _ = parent.Close() }()
	actual, err := parent.Stat()
	if err != nil {
		return nil, err
	}
	expected, err := origin.Stat()
	if err != nil || !os.SameFile(actual, expected) {
		return nil, errors.New("guardian namespace parent differs from initial namespace")
	}
	for _, name := range []string{"uid_map", "gid_map"} {
		raw, readErr := os.ReadFile("/proc/" + strconv.Itoa(opener.process.Process.Pid) + "/" + name)
		if readErr != nil || !fullIdentityMap(string(raw)) {
			return nil, errors.New("guardian namespace lacks full identity maps")
		}
	}
	return file, nil
}

func guardianRoleFD(index int) int {
	if index < 7 {
		return index + 3
	}
	return index + 4
}
