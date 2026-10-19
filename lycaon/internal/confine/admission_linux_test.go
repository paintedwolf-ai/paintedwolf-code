//go:build linux

package confine

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRequireAppliedRejectsUnavailableLinuxBoundary(t *testing.T) {
	TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	previous := linuxCapabilities()
	t.Cleanup(func() { linuxHost.caps = previous })
	for _, code := range []string{
		linuxsandbox.UnavailableUserNamespacesDenied,
		linuxsandbox.UnavailableUserNamespacesAppArmor,
		linuxsandbox.UnavailableLandlockABI,
		linuxsandbox.UnavailableSeccomp,
		linuxsandbox.UnavailableHelper,
	} {
		t.Run(code, func(t *testing.T) {
			linuxHost.caps = linuxsandbox.Capabilities{
				Unavailable: []linuxsandbox.Unavailability{{Code: code}},
			}
			if Enforcing() {
				t.Fatal("unavailable boundary reported active enforcement")
			}
			box, applied := DefaultConfinement(Request{Roots: []string{t.TempDir()}})
			if box != nil || applied {
				t.Fatal("unavailable host constructed a boundary")
			}
			err := RequireApplied(applied)
			if !errors.Is(err, ErrNotConfined) {
				t.Fatalf("required unavailable boundary = %v, want ErrNotConfined", err)
			}
			unavailable, ok := linuxsandbox.AsUnavailable(err)
			if !ok || unavailable.Code != code {
				t.Fatalf("required boundary error lost capability reason %s: %v", code, err)
			}
			box, applied = DefaultConfinement(Request{HostExecution: true})
			if box == nil || !box.HostExecution || !applied {
				t.Fatal("explicit host execution lost its distinct admission")
			}
			testutil.FailErr(t, "admit approved host execution", RequireApplied(applied))
		})
	}
}
