package browser

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"path/filepath"
	"testing"
)

// enforceConfinement puts this test in the posture where the host applies a
// boundary to every agent subprocess.
func enforceConfinement(t *testing.T) {
	t.Helper()
	if !confine.Available() {
		t.Skip("Seatbelt confinement only on darwin")
	}
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	confine.TestingSetAutoConfine(t)
}

func TestBrowserBoundaryRefusesWhenConfinementCannotBeBuilt(t *testing.T) {
	enforceConfinement(t)
	// The filesystem root cannot become a write root.
	conf, confined, err := browserBoundary([]string{string(filepath.Separator)})
	if err == nil {
		t.Fatal("browser launch fell through to an unconfined chrome while the host was enforcing confinement")
	}
	if conf != nil || confined {
		t.Fatalf("refusal returned a boundary: conf=%v confined=%v", conf, confined)
	}
	rej := &browserengine.RejectError{}
	if !errors.As(err, &rej) || rej.Code != "BROWSER_UNAVAILABLE" {
		t.Fatalf("err = %T %v, want a BROWSER_UNAVAILABLE reject", err, err)
	}
	if reason, _ := rej.Data["reason"].(string); reason != "confinement_unavailable" {
		t.Fatalf("reason = %v, want confinement_unavailable", rej.Data["reason"])
	}
}

func TestBrowserBoundaryLaunchesWhenTheHostConfinesNothing(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	conf, confined, err := browserBoundary([]string{t.TempDir()})
	testutil.FailErr(t, "boundary", err)
	if confined || conf != nil {
		t.Fatalf("sandbox off produced a boundary: conf=%v confined=%v", conf, confined)
	}
}

// The managed cache is a standing write root of every launch, so a refused one
// fails the launch instead of quietly becoming "no confinement available".
func TestLaunchHeadlessRefusesUnsafeCacheDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_BROWSER_BIN", "")
	_, _, err := LaunchHeadless(context.Background(), LaunchOptions{CacheDir: home})
	if !errors.Is(err, confine.ErrWriteRootRefused) {
		t.Fatalf("err = %v, want the cache dir refused as a write root", err)
	}
}
