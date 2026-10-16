//go:build linux

package linuxsandbox

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestClassifyPrivilegedStepNamesTheAppArmorRestriction(t *testing.T) {
	t.Parallel()
	for _, label := range []string{"unprivileged_userns", "unprivileged_userns (enforce)"} {
		got := classifyPrivilegedStep("mount /proc", unix.EPERM, label)
		if got.Code != UnavailableUserNamespacesAppArmor || got.Remedy != RemedyInstallPackage || got.Profile != AppArmorProfile {
			t.Fatalf("label %q: %+v", label, got)
		}
	}
	for _, label := range []string{"", "unconfined", "painted-wolf-code (unconfined)", "unprivileged_userns_other"} {
		if got := classifyPrivilegedStep("mount /proc", unix.EPERM, label); got.Code != UnavailableMount {
			t.Fatalf("label %q: %+v", label, got)
		}
	}
}
