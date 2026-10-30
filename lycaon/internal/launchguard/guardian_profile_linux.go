//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Ready is accepted only from the exact fork-bound process after its static
// transition. The private namespace and every currently present thread must
// carry the final caller profile before the UI admission pipe can open.
func verifyGuardianProfile(pid int, caller Identity, origin *os.File) error {
	namespace, err := os.Open(fmt.Sprintf("/proc/%d/ns/user", pid))
	if err != nil {
		return err
	}
	defer func() { _ = namespace.Close() }()
	kind, err := unix.IoctlRetInt(int(namespace.Fd()), unix.NS_GET_NSTYPE)
	if err != nil || kind != unix.CLONE_NEWUSER {
		return errors.New("guardian did not enter a user namespace")
	}
	owner, err := unix.IoctlGetInt(int(namespace.Fd()), unix.NS_GET_OWNER_UID)
	if err != nil || owner != 0 {
		return errors.New("guardian namespace is not root-created")
	}
	actual, err := namespace.Stat()
	if err != nil {
		return err
	}
	original, err := origin.Stat()
	if err != nil || os.SameFile(actual, original) {
		return errors.New("guardian remained in the initial user namespace")
	}
	parentFD, err := unix.IoctlRetInt(int(namespace.Fd()), unix.NS_GET_PARENT)
	if err != nil {
		return err
	}
	parent := os.NewFile(uintptr(parentFD), "guardian-namespace-parent")
	defer func() { _ = parent.Close() }()
	parentStat, err := parent.Stat()
	if err != nil || !os.SameFile(parentStat, original) {
		return errors.New("guardian namespace has an unexpected parent")
	}
	for _, name := range []string{"uid_map", "gid_map"} {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/%s", pid, name))
		if err != nil || !fullIdentityMap(string(raw)) {
			return errors.New("guardian namespace identity maps differ")
		}
	}
	threads, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return err
	}
	if len(threads) == 0 {
		return errors.New("guardian has no thread profile")
	}
	for _, thread := range threads {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/status", pid, thread.Name()))
		if err != nil {
			return err
		}
		if err := guardianThreadProfile(raw, caller); err != nil {
			return err
		}
	}
	return nil
}

func guardianThreadProfile(raw []byte, caller Identity) error {
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		switch key {
		case "Uid", "Gid":
			expected := caller.UID
			if key == "Gid" {
				expected = caller.GID
			}
			if seen[key] || len(fields) != 4 {
				return errors.New("guardian identity profile is malformed")
			}
			for _, field := range fields {
				actual, err := strconv.ParseUint(field, 10, 32)
				if err != nil || actual != uint64(expected) {
					return errors.New("guardian kept another thread identity")
				}
			}
		case "Groups":
			if seen[key] {
				return errors.New("duplicate guardian groups profile")
			}
			groups := make([]uint32, len(fields))
			for index, field := range fields {
				actual, err := strconv.ParseUint(field, 10, 32)
				if err != nil {
					return err
				}
				groups[index] = uint32(actual)
			}
			if !slices.Equal(groups, caller.Groups) {
				return errors.New("guardian supplementary groups differ")
			}
		case "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb":
			if seen[key] || len(fields) != 1 {
				return errors.New("guardian capability profile is malformed")
			}
			actual, err := strconv.ParseUint(fields[0], 16, 64)
			if err != nil || actual != 0 {
				return errors.New("guardian retained thread capabilities")
			}
		case "NoNewPrivs", "TracerPid":
			expected := "0"
			if key == "NoNewPrivs" {
				expected = "1"
			}
			if seen[key] || len(fields) != 1 || fields[0] != expected {
				return errors.New("guardian privilege or tracing profile differs")
			}
		default:
			continue
		}
		seen[key] = true
	}
	for _, key := range []string{"Uid", "Gid", "Groups", "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs", "TracerPid"} {
		if !seen[key] {
			return errors.New("guardian thread profile is incomplete")
		}
	}
	return nil
}
