//go:build linux

package launchguard

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"

	"golang.org/x/sys/unix"
)

const guardianRootsBytes = 64

type guardianRoots struct {
	stage    int
	leader   int
	pids     [3]int
	sequence uint64
}

func sealGuardianRoots(roots guardianRoots) (_ *os.File, err error) {
	if err := initialUserNamespace(); err != nil {
		return nil, err
	}
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		return nil, errors.New("guardian roots require initial-root stage")
	}
	if roots.stage <= 1 || roots.stage > math.MaxInt32 || roots.leader <= 1 || roots.leader > math.MaxInt32 || roots.leader != roots.stage {
		return nil, errors.New("invalid guardian stage identity")
	}
	raw := make([]byte, guardianRootsBytes)
	copy(raw, "PWGROOT1")
	binary.LittleEndian.PutUint32(raw[8:], 1)
	binary.LittleEndian.PutUint32(raw[12:], uint32(roots.stage))
	binary.LittleEndian.PutUint64(raw[16:], roots.sequence)
	binary.LittleEndian.PutUint32(raw[48:], uint32(roots.leader))
	for index, pid := range roots.pids {
		if pid <= 1 || pid > math.MaxInt32 {
			return nil, errors.New("invalid registered protected root")
		}
		binary.LittleEndian.PutUint64(raw[24+8*index:], uint64(pid))
	}
	if decoded, err := decodeGuardianRoots(raw); err != nil || decoded != roots {
		return nil, errors.New("guardian roots are not canonically representable")
	}
	fd, err := unix.MemfdCreate("painted-wolf-guardian-roots", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "sealed-guardian-roots")
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

func decodeGuardianRoots(raw []byte) (guardianRoots, error) {
	var roots guardianRoots
	if len(raw) != guardianRootsBytes || string(raw[:8]) != "PWGROOT1" || binary.LittleEndian.Uint32(raw[8:]) != 1 {
		return roots, errors.New("invalid guardian root frame")
	}
	roots.stage = int(binary.LittleEndian.Uint32(raw[12:]))
	roots.sequence = binary.LittleEndian.Uint64(raw[16:])
	roots.leader = int(binary.LittleEndian.Uint32(raw[48:]))
	if roots.stage <= 1 || roots.leader != roots.stage || roots.stage > int(^uint32(0)>>1) || roots.sequence == 0 {
		return roots, errors.New("invalid guardian stage identity")
	}
	seen := map[int]bool{roots.stage: true}
	for index := range roots.pids {
		value := binary.LittleEndian.Uint64(raw[24+8*index:])
		if value <= 1 || value > uint64(^uint32(0)>>1) || seen[int(value)] {
			return roots, errors.New("invalid registered protected root")
		}
		roots.pids[index] = int(value)
		seen[int(value)] = true
	}
	for _, value := range raw[52:] {
		if value != 0 {
			return roots, errors.New("guardian frame has reserved data")
		}
	}
	return roots, nil
}

func readGuardianRoots(file *os.File) (guardianRoots, error) {
	raw, err := readGuardianSealed(file, guardianRootsBytes)
	if err != nil {
		return guardianRoots{}, err
	}
	return decodeGuardianRoots(raw)
}

func readGuardianSealed(file *os.File, maximum int) ([]byte, error) {
	if err := rootRegular(file); err != nil {
		return nil, err
	}
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals != entrySeals {
		return nil, errors.New("guardian frame is not immutable")
	}
	raw, err := io.ReadAll(io.NewSectionReader(file, 0, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maximum {
		return nil, errors.New("guardian frame exceeds bound")
	}
	return raw, nil
}
