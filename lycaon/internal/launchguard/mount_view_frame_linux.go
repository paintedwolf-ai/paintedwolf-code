//go:build linux

package launchguard

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Only the short-lived initial-root stage publishes the startup observations.
// Readers still authenticate their namespaces, peer, entry frame and each
// transferred tree independently; this frame grants no pathname authority.
func sealModuleViewPlan(plan moduleViewPlan) (_ *os.File, err error) {
	if err := initialUserNamespace(); err != nil || os.Geteuid() != 0 {
		return nil, errors.New("module-view plan requires initial-root stage authority")
	}
	raw, err := encodeModuleViewPlan(plan)
	if err != nil {
		return nil, err
	}
	fd, err := unix.MemfdCreate("painted-wolf-module-view", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "sealed-module-view-plan")
	defer func() {
		if err != nil {
			_ = file.Close()
		}
	}()
	if _, err = file.Write(raw); err != nil {
		return nil, err
	}
	if err := file.Chmod(0o400); err != nil {
		return nil, err
	}
	_, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, entrySeals)
	return file, err
}

func readModuleViewPlan(file *os.File) (moduleViewPlan, error) {
	if err := rootRegular(file); err != nil {
		return moduleViewPlan{}, err
	}
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&entrySeals != entrySeals {
		return moduleViewPlan{}, errors.New("module-view plan is not sealed")
	}
	raw, err := io.ReadAll(io.NewSectionReader(file, 0, maxMountPlanBytes+1))
	if err != nil {
		return moduleViewPlan{}, err
	}
	return decodeModuleViewPlan(raw)
}
