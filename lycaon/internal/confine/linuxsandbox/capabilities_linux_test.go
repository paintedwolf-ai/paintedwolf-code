//go:build linux

package linuxsandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"

	"golang.org/x/sys/unix"
)

func TestNixGenerationsNameTheirExactEngineGrant(t *testing.T) {
	previous := runningNix
	t.Cleanup(func() { runningNix = previous })
	output := "/nix/store/" + strings.Repeat("0", 32) + "-painted-wolf-code-1.0.2"
	root := output + "/lib/painted-wolf-code"
	runningNix = func() (*configlayout.NixLayout, error) {
		return &configlayout.NixLayout{Output: output, Root: root}, nil
	}
	sum := sha256.Sum256([]byte(root + "/bin/pw"))
	want := Unavailability{Code: UnavailableUserNamespacesAppArmor, Detail: "restricted", Remedy: RemedyLoadEngineProfile,
		Profile: output + "/share/painted-wolf-code/apparmor/paintedwolf-engine-" + hex.EncodeToString(sum[:])}
	if got := appArmorDenied("restricted"); *got != want {
		t.Fatalf("nix generation: %+v, want %+v", *got, want)
	}
	for name, layout := range map[string]func() (*configlayout.NixLayout, error){
		"native package":    func() (*configlayout.NixLayout, error) { return nil, nil },
		"malformed receipt": func() (*configlayout.NixLayout, error) { return nil, errors.New("malformed") },
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
