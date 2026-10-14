//go:build linux

package linuxsandbox

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Landlock access sets by ABI. Every access the running ABI knows is handled,
// so anything a rule does not grant is refused.
const (
	landlockABI1Accesses = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM
	landlockReadExecute = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR
	// landlockFileAccesses are the accesses a rule on a regular file may carry.
	landlockFileAccesses = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
)

// handledAccesses lists every filesystem access the ABI can govern.
func handledAccesses(abi int) uint64 {
	handled := uint64(landlockABI1Accesses)
	if abi >= 2 {
		handled |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		handled |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		handled |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return handled
}

// landlockRule grants accesses beneath one path.
type landlockRule struct {
	path   string
	access uint64
}

// landlockRules derives the ruleset from the plan: everything readable and
// executable, write grants read-write, and the curated /dev usable.
func landlockRules(fs Filesystem, abi int) []landlockRule {
	handled := handledAccesses(abi)
	rules := []landlockRule{
		{path: "/", access: landlockReadExecute},
		{path: "/dev", access: (unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
			unix.LANDLOCK_ACCESS_FS_READ_DIR | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV) & handled},
		{path: "/dev/shm", access: handled},
	}
	for _, root := range fs.WriteRoots {
		rules = append(rules, landlockRule{path: root, access: handled})
	}
	for _, file := range fs.WriteFiles {
		rules = append(rules, landlockRule{path: file, access: landlockFileAccesses & handled})
	}
	return rules
}

// applyLandlock restricts the calling thread with the plan's rules. Paths that
// do not exist at launch get no rule, so nothing can create them.
func applyLandlock(fs Filesystem, abi int) error {
	attr := unix.LandlockRulesetAttr{Access_fs: handledAccesses(abi)}
	ruleset, err := landlockCreateRuleset(&attr, unsafe.Sizeof(attr), 0)
	if err != nil {
		return fmt.Errorf("landlock ruleset: %w", err)
	}
	defer func() { _ = unix.Close(ruleset) }()
	for _, rule := range landlockRules(fs, abi) {
		fd, err := unix.Open(rule.path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("landlock open %s: %w", rule.path, err)
		}
		access := rule.access
		if !fdIsDir(fd) {
			access &= landlockFileAccesses
		}
		beneath := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(fd)}
		err = landlockAddPathRule(ruleset, &beneath)
		_ = unix.Close(fd)
		if err != nil {
			return fmt.Errorf("landlock rule %s: %w", rule.path, err)
		}
	}
	if err := landlockRestrictSelf(ruleset); err != nil {
		return fmt.Errorf("landlock restrict_self: %w", err)
	}
	return nil
}

// The Landlock syscalls, which x/sys exposes only as numbers and types.

func landlockCreateRuleset(attr *unix.LandlockRulesetAttr, size uintptr, flags uint32) (int, error) {
	var ptr uintptr
	if attr != nil {
		ptr = uintptr(unsafe.Pointer(attr))
	}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, ptr, size, uintptr(flags))
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func landlockAddPathRule(ruleset int, attr *unix.LandlockPathBeneathAttr) error {
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(attr)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func landlockRestrictSelf(ruleset int) error {
	_, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func fdIsDir(fd int) bool {
	var st unix.Stat_t
	return unix.Fstat(fd, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR
}

// landlockProbe is a file hierarchy no rule can ever name: an unattached
// tmpfs. Creating a file in it succeeds until the ruleset applies and must
// fail with EACCES afterwards.
type landlockProbe struct{ fd int }

func newLandlockProbe() (landlockProbe, error) {
	fd, err := newTmpfs("0700")
	if err != nil {
		return landlockProbe{fd: -1}, err
	}
	probe := landlockProbe{fd: fd}
	if err := probe.create(); err != nil {
		probe.close()
		return landlockProbe{fd: -1}, fmt.Errorf("landlock probe before restriction: %w", err)
	}
	return probe, nil
}

func (p landlockProbe) create() error {
	fd, err := unix.Openat(p.fd, "probe", unix.O_CREAT|unix.O_WRONLY|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	_ = unix.Close(fd)
	return unix.Unlinkat(p.fd, "probe", 0)
}

// refused reports that the ruleset now denies the probe.
func (p landlockProbe) refused() bool {
	err := p.create()
	return errors.Is(err, unix.EACCES)
}

func (p landlockProbe) close() {
	if p.fd >= 0 {
		_ = unix.Close(p.fd)
	}
}
