//go:build linux

package launchguard

import (
	"encoding/json"
	"errors"
	"os"
	"slices"

	"golang.org/x/sys/unix"
)

// CreateEntryFrame seals the selected image and original caller into a
// root-created memfd. Only the static entry consumes this descriptor.
func CreateEntryFrame(generation *Generation, caller Identity, arguments, environment []string, broker bool) (*os.File, error) {
	if os.Geteuid() != 0 || generation == nil || generation.Desktop == nil || generation.Entry == nil || generation.Lifetime == nil {
		return nil, errors.New("entry frame requires retained privileged generation authority")
	}
	// Revalidate the original caller rather than accepting an identity supplied
	// by the unprivileged application or by a stale supervisor request.
	actual, err := CaptureCaller()
	if err != nil {
		return nil, err
	}
	if actual.UID != caller.UID || actual.GID != caller.GID || !slices.Equal(actual.Groups, caller.Groups) {
		return nil, errors.New("entry caller changed")
	}
	spec := EntrySpec{Version: 1, ParentPID: os.Getpid(), UID: caller.UID, GID: caller.GID, Image: generation.DesktopImage, Arguments: arguments, Environment: environment, Broker: broker}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxEntryBytes {
		return nil, errors.New("entry specification exceeds its bound")
	}
	if _, err := decodeEntry(raw); err != nil {
		return nil, err
	}
	fd, err := unix.MemfdCreate("painted-wolf-launch", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "sealed-entry-spec")
	success := false
	defer func() {
		if !success {
			_ = file.Close()
		}
	}()
	if _, err := file.Write(raw); err != nil {
		return nil, err
	}
	if err := file.Chmod(0400); err != nil {
		return nil, err
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, entrySeals); err != nil {
		return nil, err
	}
	if _, err := readEntry(file); err != nil {
		return nil, err
	}
	success = true
	return file, nil
}
