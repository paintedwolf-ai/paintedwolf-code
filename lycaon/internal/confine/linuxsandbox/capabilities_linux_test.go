//go:build linux

package linuxsandbox

import (
	"errors"
	"fmt"
	"testing"

	"golang.org/x/sys/unix"
)

func TestClassifyLaunchSeparatesQuotaFromHostPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		err        error
		restricted bool
		code       string
		remedy     Remedy
	}{
		{"namespace quota", unix.ENOSPC, false, UnavailableNamespaceLimit, RemedyReviewNamespaceLimits},
		{"legacy namespace quota", unix.EUSERS, false, UnavailableNamespaceLimit, RemedyReviewNamespaceLimits},
		{"quota with AppArmor", unix.ENOSPC, true, UnavailableNamespaceLimit, RemedyReviewNamespaceLimits},
		{"host policy", unix.EPERM, false, UnavailableUserNamespacesDenied, RemedyReviewHostPolicy},
		{"access denied", unix.EACCES, false, UnavailableUserNamespacesDenied, RemedyReviewHostPolicy},
		{"AppArmor restriction", unix.EPERM, true, UnavailableUserNamespacesAppArmor, RemedyInstallPackage},
		{"AppArmor access denied", unix.EACCES, true, UnavailableUserNamespacesAppArmor, RemedyInstallPackage},
		{"unsupported flags", unix.EINVAL, false, UnavailableUserNamespacesUnsupported, RemedyUpgradeKernel},
		{"unsupported syscall", unix.ENOSYS, false, UnavailableUserNamespacesUnsupported, RemedyUpgradeKernel},
		{"other errno", unix.EIO, false, UnavailableHelper, ""},
		{"untyped failure", errors.New("opaque launch failure"), true, UnavailableHelper, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyLaunch(fmt.Errorf("launch: %w", tc.err), tc.restricted)
			if got.Code != tc.code || got.Remedy != tc.remedy {
				t.Fatalf("launch failure = %+v, want %s / %s", got, tc.code, tc.remedy)
			}
			if tc.remedy == RemedyInstallPackage && got.Profile != AppArmorProfile {
				t.Fatalf("AppArmor refusal lost package profile: %+v", got)
			}
			if got.Detail == "" {
				t.Fatal("launch refusal lost diagnostics")
			}
		})
	}
}

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
