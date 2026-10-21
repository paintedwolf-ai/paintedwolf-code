//go:build linux

// Package launchguard owns the privileged bootstrap boundary for the Linux
// desktop and its protected descendants.
package launchguard

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Identity is captured from the original caller before the bootstrap forks.
// Namespace creation does not change its filesystem or desktop identity.
type Identity struct {
	UID    uint32
	GID    uint32
	Groups []uint32
}

// CaptureCaller accepts only the kernel credential transition of the installed
// setuid bootstrap. A traced or no_new_privs launch cannot obtain this state.
func CaptureCaller() (Identity, error) {
	uid, effective, saved := unix.Getresuid()
	if uid <= 0 || effective != 0 || saved != 0 {
		return Identity{}, errors.New("the installed privileged bootstrap transition is unavailable")
	}
	gid, effectiveGID, savedGID := unix.Getresgid()
	if gid < 0 || effectiveGID != gid || savedGID != gid {
		return Identity{}, errors.New("bootstrap group identity differs from the original caller")
	}
	groups, err := os.Getgroups()
	if err != nil {
		return Identity{}, err
	}
	caller := Identity{UID: uint32(uid), GID: uint32(gid)}
	for _, group := range groups {
		caller.Groups = append(caller.Groups, uint32(group))
	}
	return caller, nil
}

// Attributes creates a user namespace owned by the privileged parent. The
// entry executable must remove all capabilities and set no_new_privs before
// executing application code: identity mappings deliberately preserve root
// ownership facts, not authority to assume uid 0.
func Attributes(caller Identity) (*syscall.SysProcAttr, error) {
	if os.Geteuid() != 0 || uint32(os.Getuid()) != caller.UID || uint32(os.Getgid()) != caller.GID || caller.UID == 0 || caller.UID == ^uint32(0) || caller.GID == ^uint32(0) {
		return nil, errors.New("protected launch requires a privileged bootstrap and a login user")
	}
	return &syscall.SysProcAttr{
		Cloneflags:                 unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
		GidMappingsEnableSetgroups: true,
		Credential:                 &syscall.Credential{Uid: 0, Gid: 0},
		Pdeathsig:                  syscall.SIGKILL,
	}, nil
}

// SealEntry removes capabilities and privilege-changing exec before any
// application configuration or libraries can be consumed.
func SealEntry(originNamespace *os.File) error {
	if os.Getuid() == 0 || os.Getuid() != os.Geteuid() || os.Getgid() != os.Getegid() {
		return errors.New("entry credentials are not the original login identity")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("seal privilege transitions: %w", err)
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return fmt.Errorf("clear ambient capabilities: %w", err)
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var capabilities [2]unix.CapUserData
	if err := unix.Capset(&header, &capabilities[0]); err != nil {
		return fmt.Errorf("clear capabilities: %w", err)
	}
	if err := verifyNamespace(originNamespace); err != nil {
		return err
	}
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}

func verifyNamespace(origin *os.File) error {
	if origin == nil {
		return errors.New("missing original user namespace")
	}
	kind, err := unix.IoctlGetInt(int(origin.Fd()), unix.NS_GET_NSTYPE)
	if err != nil || kind != unix.CLONE_NEWUSER {
		return errors.New("original descriptor is not a user namespace")
	}
	current, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	owner, err := unix.IoctlGetInt(int(current.Fd()), unix.NS_GET_OWNER_UID)
	if err != nil || owner != 0 {
		return errors.New("user namespace was not created by the privileged bootstrap")
	}
	actual, err := current.Stat()
	if err != nil {
		return err
	}
	original, err := origin.Stat()
	if err != nil {
		return err
	}
	if os.SameFile(actual, original) {
		return errors.New("entry did not cross the protected namespace boundary")
	}
	for _, name := range []string{"uid_map", "gid_map"} {
		raw, err := os.ReadFile("/proc/self/" + name)
		if err != nil {
			return err
		}
		if !fullIdentityMap(string(raw)) {
			return fmt.Errorf("%s is not a full identity mapping", name)
		}
	}
	return nil
}

func fullIdentityMap(raw string) bool {
	fields := strings.Fields(raw)
	if len(fields) != 3 {
		return false
	}
	for i, want := range []uint64{0, 0, uint64(^uint32(0))} {
		got, err := strconv.ParseUint(fields[i], 10, 32)
		if err != nil || got != want {
			return false
		}
	}
	return true
}
