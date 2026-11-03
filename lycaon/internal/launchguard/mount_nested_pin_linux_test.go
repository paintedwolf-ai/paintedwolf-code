//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// An owned tmpfs observation pin prevents mount-ID recycling during this one
// replacement. It is closed before source unmount and is not leak/latency evidence.
func pinNestedViewRoot(source, desktop *os.File, pins *fixtureLifecyclePins, target string, expected int, deadline time.Time) (fd int, err error) {
	fd = -1
	if err := nestedRolesAlive(pins); err != nil {
		return fd, err
	}
	birth, err := lifecycleBirth(pins.roles[0].PID)
	if err != nil || birth.Start != pins.roles[0].Start || birth.Parent != pins.roles[0].Parent {
		return -1, errors.New("desktop birth differs before view pin")
	}
	if err := unix.Setns(int(desktop.Fd()), unix.CLONE_NEWNS); err != nil {
		return -1, err
	}
	defer func() {
		err = errors.Join(err, unix.Setns(int(source.Fd()), unix.CLONE_NEWNS))
		after, birthErr := lifecycleBirth(pins.roles[0].PID)
		if birthErr != nil || after != birth {
			err = errors.Join(err, errors.New("desktop birth changed during view pin"))
		}
		err = errors.Join(err, nestedRolesAlive(pins))
		if err != nil && fd >= 0 {
			_ = unix.Close(fd)
			fd = -1
		}
	}()
	// An epoch notification invalidates only this observation. Preserve the
	// exact selected mount and retry capture within the existing setup bound.
	for time.Now().Before(deadline) {
		if err := nestedRolesAlive(pins); err != nil {
			return -1, err
		}
		var changed bool
		fd, changed, err = captureNestedViewPin(target, expected)
		if err != nil || !changed {
			return fd, err
		}
		time.Sleep(time.Millisecond)
	}
	return -1, errors.New("view pin did not reach a stable topology before setup deadline")
}

// One capture owns its poll descriptor and discards every pin from a changed epoch.
func captureNestedViewPin(target string, expected int) (fd int, changed bool, err error) {
	fd = -1
	defer func() {
		if (changed || err != nil) && fd >= 0 {
			err = errors.Join(err, unix.Close(fd))
			fd = -1
		}
	}()
	info, err := unix.Open("/proc/thread-self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, false, err
	}
	defer unix.Close(info)
	rows, changed, err := readIntakeMountTopology(info, readMountInfo)
	if err != nil || changed {
		return -1, changed, err
	}
	selected := false
	for _, row := range rows {
		if row.id == expected && row.mountPoint == target {
			selected = true
		}
	}
	if !selected {
		return -1, false, errors.New("view pin topology lacks exact selected mount")
	}
	fd, err = openMountPoint(target)
	if err != nil {
		return -1, false, err
	}
	id, inode, err := mountDescriptorIdentity(fd)
	if err != nil {
		return fd, false, err
	}
	changed, err = mountInfoEpochChanged(info)
	if err != nil || changed {
		return fd, changed, err
	}
	if id != uint64(expected) {
		return fd, false, errors.New("view pin changed exact mount identity")
	}
	fmt.Printf("PW_NESTED_PIN01 {\"mount_id\":%d,\"inode\":%d}\n", id, inode)
	return fd, false, nil
}
