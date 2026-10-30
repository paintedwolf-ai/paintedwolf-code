//go:build linux

package launchguard

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

const guardianSlotFixture = "PW_GUARDIAN_SLOT_FIXTURE"

func init() {
	// The reservation contract requires the original process leader. Go runs
	// initialization on that thread; retain it only in the owned fixture child.
	if os.Getenv(guardianSlotFixture) != "" {
		runtime.LockOSThread()
	}
}

func TestMain(m *testing.M) {
	if mode := os.Getenv(guardianSlotFixture); mode != "" {
		if runGuardianSlotFixture(mode) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestGuardianExecSlotsPreserveForeignDescriptors(t *testing.T) {
	for _, mode := range []string{"collision-64", "collision-85", "collision-86", "close", "exec-failure"} {
		t.Run(mode, func(t *testing.T) {
			command := exec.Command("sudo", "-n", "env", guardianSlotFixture+"="+mode, os.Args[0], "-test.run=^$")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("actual initial-root slot fixture %s: %v %s", mode, err, output)
			}
		})
	}
}

func runGuardianSlotFixture(mode string) error {
	if err := initialUserNamespace(); err != nil {
		return err
	}
	uid, euid, suid := unix.Getresuid()
	gid, egid, sgid := unix.Getresgid()
	if uid != 0 || euid != 0 || suid != 0 || gid != 0 || egid != 0 || sgid != 0 || unix.Gettid() != os.Getpid() {
		return errors.New("fixture lacks actual initial-root leader identity")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	defer writer.Close()
	collision := map[string]int{"collision-64": 64, "collision-85": guardianIncomingProof, "collision-86": guardianIncomingImage}[mode]
	if collision != 0 {
		for fd := 64; fd <= guardianIncomingImage; fd++ {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
				return errors.New("fixture transition slot already occupied: " + strconv.Itoa(fd))
			}
		}
		foreign, err := unix.FcntlInt(writer.Fd(), unix.F_DUPFD_CLOEXEC, collision)
		if err != nil {
			return err
		}
		defer unix.Close(foreign)
		if foreign != collision {
			return errors.New("fixture could not reserve its collision slot")
		}
		before, err := unix.FcntlInt(uintptr(foreign), unix.F_GETFD, 0)
		if err != nil {
			return err
		}
		if slots, err := reserveGuardianExecSlots(); err == nil {
			slots.close()
			return errors.New("reservation overwrote foreign slot")
		}
		after, err := unix.FcntlInt(uintptr(foreign), unix.F_GETFD, 0)
		if err != nil || before != after {
			return errors.New("collision changed foreign descriptor flags")
		}
		for fd := 64; fd <= guardianIncomingImage; fd++ {
			if fd == foreign {
				continue
			}
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
				return errors.New("failed reservation retained owned slot: " + strconv.Itoa(fd))
			}
		}
		if _, err := unix.Write(foreign, []byte{'f'}); err != nil {
			return err
		}
	} else {
		null, err := os.Open("/dev/null")
		if err != nil {
			return err
		}
		defer null.Close()
		slots, err := reserveGuardianExecSlots()
		if err != nil {
			return err
		}
		defer slots.close()
		if mode == "exec-failure" {
			fd, err := unix.MemfdCreate("invalid-guardian-fixture", unix.MFD_CLOEXEC)
			if err != nil {
				return err
			}
			image := os.NewFile(uintptr(fd), "invalid-executable")
			defer image.Close()
			if _, err := image.WriteString("not an executable\n"); err != nil {
				return err
			}
			if err := image.Chmod(0500); err != nil {
				return err
			}
			var roles [guardianTransitionRoles]*os.File
			for index := range roles {
				roles[index] = null
			}
			if err := slots.execute(roles, null, image); !errors.Is(err, unix.ENOEXEC) {
				return errors.New("fixture did not reach actual exec format refusal")
			}
		} else if mode != "close" {
			return errors.New("unknown slot fixture mode")
		}
		slots.close()
		for fd := 64; fd <= guardianIncomingImage; fd++ {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
				return errors.New("owned transition descriptor survived close")
			}
		}
		if _, err := writer.Write([]byte{'f'}); err != nil {
			return err
		}
	}
	var value [1]byte
	if _, err := reader.Read(value[:]); err != nil {
		return err
	}
	if value[0] != 'f' {
		return errors.New("foreign pipe changed identity")
	}
	return nil
}
