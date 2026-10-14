//go:build linux

package linuxsandbox

import (
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// Enter applies the plan to the calling thread's process, which must already
// be PID 1 of fresh user, mount, PID, and network namespaces. On return the
// thread is restricted and attested; a child it forks inherits everything.
func Enter(plan Plan) error {
	// Landlock restricts the calling thread, so the thread that forks the
	// target must be the one restricted here.
	runtime.LockOSThread()
	if err := plan.Validate(); err != nil {
		return err
	}
	facts := Facts()
	if !facts.FloorMet() {
		u := facts.Unavailable[0]
		return &u
	}
	if err := mountProc(); err != nil {
		return err
	}
	if err := namespacesDiffer(plan.Attest.HostNamespaces); err != nil {
		return err
	}
	if err := makeRootPrivate(); err != nil {
		return err
	}
	if err := applyFilesystem(plan.Filesystem); err != nil {
		return err
	}
	if err := mountDev(); err != nil {
		return err
	}
	if err := loopbackUp(); err != nil {
		return err
	}
	probe, err := newLandlockProbe()
	if err != nil {
		return err
	}
	defer probe.close()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if plan.Attest.Omit != OmitLandlock {
		if err := applyLandlock(plan.Filesystem, facts.LandlockABI); err != nil {
			return err
		}
	}
	if plan.Attest.Omit != OmitSeccomp {
		if err := applySeccomp(); err != nil {
			return err
		}
	}
	if err := dropCapabilities(); err != nil {
		return err
	}
	return attest(plan, probe)
}

// dropCapabilities leaves the process, and so the target, with no capability
// in the user namespace it owns.
func dropCapabilities() error {
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return fmt.Errorf("clear ambient capabilities: %w", err)
	}
	for cap := uintptr(0); cap <= unix.CAP_LAST_CAP; cap++ {
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, cap, 0, 0, 0); err != nil && !errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("drop bounding capability %d: %w", cap, err)
		}
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capset(&header, &data[0]); err != nil {
		return fmt.Errorf("clear capabilities: %w", err)
	}
	return nil
}

// attest confirms the kernel facts before anything runs: the Landlock domain
// refuses, the seccomp filter is installed, privileges cannot grow, no
// capability remains, and every namespace differs from the host's.
func attest(plan Plan, probe landlockProbe) error {
	var failed []string
	if !probe.refused() {
		failed = append(failed, "landlock ruleset is not applied")
	}
	if mode, err := seccompMode(); err != nil || mode != 2 {
		failed = append(failed, "seccomp filter is not installed")
	}
	if nnp, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0); err != nil || nnp != 1 {
		failed = append(failed, "no_new_privs is not set")
	}
	if !capabilitiesEmpty() {
		failed = append(failed, "capabilities remain")
	}
	if err := namespacesDiffer(plan.Attest.HostNamespaces); err != nil {
		failed = append(failed, err.Error())
	}
	if len(failed) > 0 {
		return errors.New("linux sandbox attestation failed: " + strings.Join(failed, "; ") + " — refusing to run unconfined")
	}
	return nil
}

func capabilitiesEmpty() bool {
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&header, &data[0]); err != nil {
		return false
	}
	for _, d := range data {
		if d.Effective != 0 || d.Permitted != 0 || d.Inheritable != 0 {
			return false
		}
	}
	return true
}
