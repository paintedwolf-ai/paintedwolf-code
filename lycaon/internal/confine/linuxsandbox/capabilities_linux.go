//go:build linux

package linuxsandbox

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Kernel switches that govern unprivileged user namespaces.
const (
	appArmorUsernsSysctl  = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"
	usernsCloneSysctl     = "/proc/sys/kernel/unprivileged_userns_clone"
	maxUserNamespacesFile = "/proc/sys/user/max_user_namespaces"
	appArmorLabelFile     = "/proc/self/attr/apparmor/current"
	selinuxEnforceFile    = "/sys/fs/selinux/enforce"
)

// Facts reads the kernel facts available without creating namespaces. Whether
// namespaces can be created is only known from a helper run; RecordRun adds it.
func Facts() Capabilities {
	c := Capabilities{
		AppArmorRestrictsUserns: sysctlIs(appArmorUsernsSysctl, 1),
		SELinuxEnforcing:        sysctlIs(selinuxEnforceFile, 1),
		AppArmorLabel:           appArmorLabel(),
	}
	abi, err := landlockABI()
	switch {
	case err != nil:
		c.Unavailable = append(c.Unavailable, Unavailability{Code: UnavailableLandlock, Detail: err.Error(), Remedy: RemedyUpgradeKernel})
	case abi < LandlockABIFloor:
		c.LandlockABI = abi
		c.Unavailable = append(c.Unavailable, Unavailability{
			Code: UnavailableLandlockABI, Detail: fmt.Sprintf("ABI %d, floor %d", abi, LandlockABIFloor), Remedy: RemedyUpgradeKernel,
		})
	default:
		c.LandlockABI = abi
	}
	if err := seccompAvailable(); err != nil {
		c.Unavailable = append(c.Unavailable, Unavailability{Code: UnavailableSeccomp, Detail: err.Error(), Remedy: RemedyUpgradeKernel})
	} else {
		c.Seccomp = true
	}
	if fd, err := unix.PidfdOpen(os.Getpid(), 0); err != nil {
		c.Unavailable = append(c.Unavailable, Unavailability{Code: UnavailablePidfd, Detail: err.Error(), Remedy: RemedyUpgradeKernel})
	} else {
		_ = unix.Close(fd)
		c.Pidfd = true
	}
	if sysctlIs(usernsCloneSysctl, 0) || sysctlIs(maxUserNamespacesFile, 0) {
		c.Unavailable = append(c.Unavailable, Unavailability{
			Code: UnavailableUserNamespacesDenied, Detail: "unprivileged user namespaces are disabled by sysctl", Remedy: RemedyEnableUserNamespaces,
		})
	}
	return c
}

// FloorMet reports whether every fact readable in-process meets the floor.
func (c Capabilities) FloorMet() bool { return len(c.Unavailable) == 0 }

// landlockABI asks the kernel which Landlock ABI it implements.
func landlockABI() (int, error) {
	abi, err := landlockCreateRuleset(nil, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if err != nil {
		if errors.Is(err, unix.EOPNOTSUPP) {
			return 0, errors.New("landlock is disabled at boot (lsm= does not list it)")
		}
		return 0, fmt.Errorf("landlock: %w", err)
	}
	return abi, nil
}

// seccompAvailable checks that the kernel can kill a process from a filter.
func seccompAvailable() error {
	action := uint32(unix.SECCOMP_RET_KILL_PROCESS)
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_GET_ACTION_AVAIL, 0, uintptr(unsafe.Pointer(&action)))
	if errno != 0 {
		return fmt.Errorf("seccomp: %w", errno)
	}
	return nil
}

func sysctlIs(path string, want int) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	return err == nil && value == want
}

func appArmorLabel() string {
	raw, err := os.ReadFile(appArmorLabelFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// HostNamespaces reads this process's namespace identities, as the plan
// records them for attestation.
func HostNamespaces() map[string]string {
	out := map[string]string{}
	for _, kind := range NamespaceKinds() {
		if id, err := os.Readlink("/proc/self/ns/" + kind); err == nil {
			out[kind] = id
		}
	}
	return out
}

// classifyLaunch names why the kernel refused to create the helper's namespaces.
func classifyLaunch(err error) *Unavailability {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return &Unavailability{Code: UnavailableHelper, Detail: err.Error()}
	}
	switch {
	case errors.Is(errno, unix.EPERM) && sysctlIs(appArmorUsernsSysctl, 1):
		return appArmorDenied("AppArmor restricts unprivileged user namespaces to profiled programs")
	case errors.Is(errno, unix.EPERM), errors.Is(errno, unix.EACCES), errors.Is(errno, unix.ENOSPC), errors.Is(errno, unix.EUSERS):
		return &Unavailability{Code: UnavailableUserNamespacesDenied, Detail: errno.Error(), Remedy: RemedyEnableUserNamespaces}
	case errors.Is(errno, unix.EINVAL), errors.Is(errno, unix.ENOSYS):
		return &Unavailability{Code: UnavailableUserNamespacesUnsupported, Detail: errno.Error(), Remedy: RemedyUpgradeKernel}
	}
	return &Unavailability{Code: UnavailableHelper, Detail: err.Error()}
}

// unprivilegedUsernsLabel is the AppArmor profile an unprofiled program enters
// when it creates a user namespace on a host that restricts them. The profile
// holds no capabilities, so namespace creation succeeds and the first mount fails.
const unprivilegedUsernsLabel = "unprivileged_userns"

// classifyPrivilegedStep names why a step needing the namespace's own
// capabilities failed, given the AppArmor label the helper runs under there.
func classifyPrivilegedStep(step string, err error, label string) *Unavailability {
	if label == unprivilegedUsernsLabel || strings.HasPrefix(label, unprivilegedUsernsLabel+" ") {
		return appArmorDenied(step + ": " + err.Error() + " under AppArmor label " + label)
	}
	return &Unavailability{Code: UnavailableMount, Detail: step + ": " + err.Error()}
}

func appArmorDenied(detail string) *Unavailability {
	return &Unavailability{
		Code:    UnavailableUserNamespacesAppArmor,
		Detail:  detail,
		Remedy:  RemedyInstallPackage,
		Profile: AppArmorProfile,
	}
}
