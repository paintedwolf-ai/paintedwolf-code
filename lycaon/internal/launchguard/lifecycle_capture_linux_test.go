//go:build linux

package launchguard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type lifecycleRole struct {
	Role   string `json:"role"`
	PID    int    `json:"pid"`
	Parent int    `json:"parent"`
	Start  uint64 `json:"start"`
}

type fixtureLifecyclePins struct {
	roles []lifecycleRole
	pins  []int
}

func (owned *fixtureLifecyclePins) close() {
	for _, pin := range owned.pins {
		_ = unix.Close(pin)
	}
}

// Capture the exact selected guardian slots once for independent fixture observers.
func captureFixtureLifecyclePins() (_ *fixtureLifecyclePins, err error) {
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		return nil, errors.New("lifecycle observer requires initial-root fixture inspection")
	}
	pid, err := strconv.Atoi(os.Getenv(fixtureDesktopPIDEnv))
	if err != nil || pid <= 1 {
		return nil, errors.New("invalid fixture desktop PID")
	}
	start, err := strconv.ParseUint(os.Getenv("PW_FIXTURE_DESKTOP_START"), 10, 64)
	if err != nil || start == 0 {
		return nil, errors.New("missing creator-recorded desktop birth")
	}
	uid, err := strconv.ParseUint(os.Getenv(fixtureUIDEnv), 10, 32)
	if err != nil || uid == 0 {
		return nil, errors.New("invalid fixture caller identity")
	}
	bootstrap, err := strconv.Atoi(os.Getenv("PW_FIXTURE_BOOTSTRAP_PID"))
	if err != nil || bootstrap <= 1 {
		return nil, errors.New("invalid owned fixture bootstrap")
	}
	policyPath = filepath.Join(os.Getenv(fixtureDirEnv), "launch.json")
	lockDirectory = filepath.Join(os.Getenv(fixtureDirEnv), "locks")
	generation, err := CaptureGeneration()
	if err != nil {
		return nil, err
	}
	defer generation.Close()
	desktop, err := lifecycleBirth(pid)
	if err != nil || desktop.Start != start {
		return nil, errors.New("desktop birth differs from launch report")
	}
	guardian, err := lifecycleBirth(desktop.Parent)
	if err != nil || guardian.Parent != bootstrap {
		return nil, errors.New("guardian is not the owned bootstrap child")
	}
	guardFD, err := unix.PidfdOpen(guardian.PID, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(guardFD)
	guardDir, err := unix.Open(fmt.Sprintf("/proc/%d", guardian.PID), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(guardDir)
	if err := verifyFixtureBirth(guardDir, guardian.PID, guardian.Start, uint32(uid)); err != nil {
		return nil, err
	}
	matched, err := matchFixtureMountRole(guardDir, bootstrap, GuardianArgument, generation.Entry)
	if err != nil || !matched {
		return nil, errors.Join(errors.New("guardian image/role differs from owned selected entry"), err)
	}
	var owned fixtureLifecyclePins
	defer func() {
		if err != nil {
			owned.close()
		}
	}()
	for index, name := range []string{"desktop", "mount-watcher", "mount-intake"} {
		pin, err := unix.PidfdGetfd(guardFD, 15+index, 0)
		if err != nil {
			return nil, fmt.Errorf("duplicate authenticated %s pidfd: %w", name, err)
		}
		owned.pins = append(owned.pins, pin)
		rolePID, err := lifecyclePIDFDIdentity(pin)
		if err != nil {
			return nil, err
		}
		birth, err := lifecycleBirth(rolePID)
		if err != nil || birth.Parent != guardian.PID {
			return nil, errors.New("registered role has another guardian")
		}
		birth.Role = name
		directory, err := unix.Open(fmt.Sprintf("/proc/%d", rolePID), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		verifyErr := verifyFixtureBirth(directory, rolePID, birth.Start, uint32(uid))
		if verifyErr == nil && index == 0 {
			if rolePID != pid || birth.Start != start {
				verifyErr = errors.New("desktop pidfd differs from exact fixture birth")
			} else {
				verifyErr = lifecycleDesktopImage(rolePID, generation.Desktop)
			}
		} else if verifyErr == nil {
			role := MountViewArgument
			if index == 2 {
				role = MountIntakeArgument
			}
			matched, matchErr := matchFixtureMountRole(directory, guardian.PID, role, generation.Entry)
			if matchErr != nil || !matched {
				verifyErr = fmt.Errorf("registered %s image/role differs: %v", name, matchErr)
			}
		}
		_ = unix.Close(directory)
		if verifyErr != nil {
			return nil, verifyErr
		}
		owned.roles = append(owned.roles, birth)
	}
	currentGuardian, err := lifecycleBirth(guardian.PID)
	if err != nil || currentGuardian != guardian {
		return nil, errors.New("guardian birth changed during pidfd authentication")
	}
	// Authentication no longer needs the selected image/lock descriptors.
	// Close them before admission so only pidfds survive into observation.
	generation.Close()
	return &owned, nil
}

func lifecyclePIDFDIdentity(fd int) (int, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && key == "Pid" {
			pid, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || pid <= 1 {
				return 0, errors.New("authenticated pidfd has no living PID")
			}
			return pid, nil
		}
	}
	return 0, errors.New("authenticated descriptor is not a pidfd")
}

func lifecycleBirth(pid int) (lifecycleRole, error) {
	role := lifecycleRole{PID: pid}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return role, err
	}
	begin, end := bytes.IndexByte(raw, '('), bytes.LastIndexByte(raw, ')')
	if begin < 1 || end < begin {
		return role, errors.New("process birth lacks command field")
	}
	actual, err := strconv.Atoi(strings.TrimSpace(string(raw[:begin])))
	if err != nil || actual != pid {
		return role, errors.New("process birth names another PID")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return role, errors.New("process birth is incomplete")
	}
	role.Parent, err = strconv.Atoi(fields[1])
	if err != nil {
		return role, err
	}
	role.Start, err = strconv.ParseUint(fields[19], 10, 64)
	return role, err
}

func lifecycleDesktopImage(pid int, selected *os.File) error {
	file, err := os.Open(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	wanted, err := selected.Stat()
	if err != nil || !os.SameFile(actual, wanted) {
		return errors.New("observed desktop image differs from selected fixture generation")
	}
	return nil
}
