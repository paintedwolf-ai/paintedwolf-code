package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type dedicatedProtection struct {
	uid, gid                                             int
	groups                                               []int
	effective, permitted, inheritable, bounding, ambient uint64
	noNewPrivileges                                      int
}

func requireDedicatedProtection() error {
	facts, err := readDedicatedProtection()
	if err != nil {
		return fmt.Errorf("read dedicated service protection: %w", err)
	}
	return facts.validate()
}

func readDedicatedProtection() (dedicatedProtection, error) {
	facts := dedicatedProtection{uid: os.Getuid(), gid: os.Getgid()}
	var err error
	facts.groups, err = os.Getgroups()
	if err != nil {
		return facts, err
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var caps [2]unix.CapUserData
	if err := unix.Capget(&header, &caps[0]); err != nil {
		return facts, err
	}
	facts.effective = uint64(caps[0].Effective) | uint64(caps[1].Effective)<<32
	facts.permitted = uint64(caps[0].Permitted) | uint64(caps[1].Permitted)<<32
	facts.inheritable = uint64(caps[0].Inheritable) | uint64(caps[1].Inheritable)<<32
	for capability := 0; capability < 64; capability++ {
		bound, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, uintptr(capability), 0, 0, 0)
		if errors.Is(err, unix.EINVAL) {
			break
		}
		if err != nil {
			return facts, err
		}
		ambient, err := unix.PrctlRetInt(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_IS_SET, uintptr(capability), 0, 0)
		if err != nil {
			return facts, err
		}
		if bound != 0 {
			facts.bounding |= uint64(1) << capability
		}
		if ambient != 0 {
			facts.ambient |= uint64(1) << capability
		}
	}
	facts.noNewPrivileges, err = unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	return facts, err
}

func (facts dedicatedProtection) validate() error {
	const ptrace = uint64(1) << unix.CAP_SYS_PTRACE
	if facts.uid == 0 || facts.gid == 0 || len(facts.groups) != 0 || facts.noNewPrivileges != 1 || facts.effective != ptrace || facts.permitted != ptrace || facts.inheritable != ptrace || facts.bounding != ptrace || facts.ambient != ptrace {
		return errors.New("dedicated service requires non-root uid/gid, no supplementary groups, no-new-privileges and only CAP_SYS_PTRACE in every capability set")
	}
	return nil
}
