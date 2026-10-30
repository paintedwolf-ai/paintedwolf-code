//go:build linux

package launchguard

import (
	"errors"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

const guardianIncomingBase = 61
const guardianIncomingProof = 85
const guardianIncomingImage = 86

type guardianExecSlots struct{ descriptors []int }

// Reserve before setup opens many descriptors. Never overwrite a descriptor
// owned by the Go runtime: collision refuses startup and leaves it untouched.
func reserveGuardianExecSlots() (_ *guardianExecSlots, err error) {
	if err := initialUserNamespace(); err != nil {
		return nil, err
	}
	if os.Getuid() != 0 || os.Geteuid() != 0 || unix.Gettid() != os.Getpid() {
		return nil, errors.New("guardian setup requires the initial-root process leader")
	}
	null, err := unix.Open("/dev/null", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(null) }()
	var stat unix.Stat_t
	if err := unix.Fstat(null, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR || stat.Rdev != unix.Mkdev(1, 3) || stat.Uid != 0 {
		return nil, errors.New("guardian descriptor reservation lacks the fixed null device")
	}
	slots := &guardianExecSlots{}
	defer func() {
		if err != nil {
			slots.close()
		}
	}()
	for expected := 64; expected <= guardianIncomingImage; expected++ {
		fd, err := unix.FcntlInt(uintptr(null), unix.F_DUPFD_CLOEXEC, expected)
		if err != nil {
			return nil, err
		}
		if fd != expected {
			_ = unix.Close(fd)
			return nil, errors.New("guardian transition descriptor slot already owned")
		}
		slots.descriptors = append(slots.descriptors, fd)
	}
	return slots, nil
}

func (slots *guardianExecSlots) close() {
	for _, fd := range slots.descriptors {
		_ = unix.Close(fd)
	}
	slots.descriptors = nil
}

// Only reserved high slots change while Go is running. The freestanding
// transition remaps them after exec has removed every other runtime thread.
func (slots *guardianExecSlots) execute(roles [guardianTransitionRoles]*os.File, proof, image *os.File) error {
	if unix.Gettid() != os.Getpid() || len(slots.descriptors) != guardianIncomingImage-64+1 {
		return errors.New("guardian exec lost its process leader or reserved slots")
	}
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	for index, file := range roles {
		if file == nil {
			return errors.New("guardian exec lacks a fixed role")
		}
		if err := unix.Dup3(int(file.Fd()), guardianRoleFD(index)+guardianIncomingBase, 0); err != nil {
			return err
		}
	}
	if err := unix.Dup3(int(proof.Fd()), guardianIncomingProof, 0); err != nil {
		return err
	}
	if err := unix.Dup3(int(image.Fd()), guardianIncomingImage, 0); err != nil {
		return err
	}
	// #nosec G204 -- fd86 pins the authenticated root-owned image; fixed argv and empty env avoid PATH lookup.
	return syscall.Exec("/proc/self/fd/"+strconv.Itoa(guardianIncomingImage), []string{"pw-mount-intake-transition"}, []string{})
}
