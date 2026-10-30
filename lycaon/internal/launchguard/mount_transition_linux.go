//go:build linux

package launchguard

import (
	"encoding/binary"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// The fixed native helper binds the already validated stage selections before
// joining U_view. This ephemeral binary frame changes no durable manifest.
const mountTransitionBytes = 184

func sealMountTransition(roles [7]*os.File) (_ *os.File, err error) {
	if uid, effective, saved := unix.Getresuid(); uid != 0 || effective != 0 || saved != 0 {
		return nil, errors.New("mount transition requires the privileged stage")
	}
	if gid, effective, saved := unix.Getresgid(); gid != 0 || effective != 0 || saved != 0 {
		return nil, errors.New("mount transition requires the privileged stage group")
	}
	raw := make([]byte, mountTransitionBytes)
	copy(raw, []byte{'P', 'W', 'M', 'I', 'N', '0', '1', 0})
	binary.LittleEndian.PutUint32(raw[8:], 1)
	binary.LittleEndian.PutUint32(raw[12:], uint32(len(roles)))
	for index, file := range roles {
		if file == nil {
			return nil, errors.New("mount transition lacks a retained role")
		}
		var stat unix.Statx_t
		if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS, &stat); err != nil {
			return nil, err
		}
		if stat.Mask&unix.STATX_INO == 0 || stat.Ino == 0 {
			return nil, errors.New("mount transition role has no kernel identity")
		}
		row := raw[16+24*index:]
		binary.LittleEndian.PutUint32(row, uint32(3+index))
		binary.LittleEndian.PutUint64(row[8:], uint64(stat.Dev_major)<<32|uint64(stat.Dev_minor))
		binary.LittleEndian.PutUint64(row[16:], stat.Ino)
	}
	fd, err := unix.MemfdCreate("painted-wolf-mount-transition", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	frame := os.NewFile(uintptr(fd), "sealed-mount-transition")
	defer func() {
		if err != nil {
			_ = frame.Close()
		}
	}()
	if _, err = frame.Write(raw); err != nil {
		return nil, err
	}
	if err := frame.Chmod(0o400); err != nil {
		return nil, err
	}
	_, err = unix.FcntlInt(frame.Fd(), unix.F_ADD_SEALS, entrySeals)
	if err != nil {
		return nil, err
	}
	return frame, nil
}
