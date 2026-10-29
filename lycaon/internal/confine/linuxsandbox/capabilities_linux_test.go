//go:build linux

package linuxsandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/nixgeneration"

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
			t.Parallel()
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

func TestNixGenerationsNameTheirExactEngineGrant(t *testing.T) {
	previous := runningNix
	t.Cleanup(func() { runningNix = previous })
	output := "/nix/store/" + strings.Repeat("0", 32) + "-painted-wolf-code-1.0.2"
	root := output + "/lib/painted-wolf-code"
	runningNix = func() (*nixgeneration.Layout, error) {
		return &nixgeneration.Layout{Output: output, Root: root}, nil
	}
	sum := sha256.Sum256([]byte(root + "/bin/pw"))
	want := Unavailability{Code: UnavailableUserNamespacesAppArmor, Detail: "restricted", Remedy: RemedyLoadEngineProfile,
		Profile: output + "/share/painted-wolf-code/apparmor/paintedwolf-engine-" + hex.EncodeToString(sum[:])}
	if got := appArmorDenied("restricted"); *got != want {
		t.Fatalf("nix generation: %+v, want %+v", *got, want)
	}
	for name, layout := range map[string]func() (*nixgeneration.Layout, error){
		"native package":    func() (*nixgeneration.Layout, error) { return nil, nil },
		"malformed receipt": func() (*nixgeneration.Layout, error) { return nil, errors.New("malformed") },
	} {
		runningNix = layout
		if got := appArmorDenied("restricted"); got.Remedy != RemedyInstallPackage || got.Profile != AppArmorProfile {
			t.Fatalf("%s: %+v", name, *got)
		}
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
