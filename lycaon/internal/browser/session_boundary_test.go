package browser

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"os"
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
	roots := protectedBrowserRoots(t)
	conf, confined, err := browserBoundary([]string{roots[0]})
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
	for _, root := range protectedBrowserRoots(t) {
		t.Run(filepath.Base(root), func(t *testing.T) {
			assertLaunchWriteRootRefused(t, LaunchOptions{CacheDir: root}, root, confine.WriteRootCodeSecretStore)
		})
	}
	t.Run("relative", func(t *testing.T) {
		assertLaunchWriteRootRefused(t, LaunchOptions{CacheDir: "relative-cache"}, "relative-cache", confine.WriteRootCodeNotAbsolute)
	})
}

func protectedBrowserRoots(t *testing.T) []string {
	t.Helper()
	base := t.TempDir()
	keys, credentials := filepath.Join(base, "keys"), filepath.Join(base, "credentials")
	testutil.FailErr(t, "create key store", os.Mkdir(keys, 0o700))
	testutil.FailErr(t, "create credential store", os.Mkdir(credentials, 0o700))
	previousKeys, previousCredentials := confine.KeyMaterialWritePaths(), confine.CredentialStorePaths()
	confine.SetKeyMaterialPathsSource(func() []string { return []string{keys} })
	confine.SetCredentialStorePathsSource(func() []string { return []string{credentials} })
	t.Cleanup(func() {
		confine.SetKeyMaterialPathsSource(func() []string { return previousKeys })
		confine.SetCredentialStorePathsSource(func() []string { return previousCredentials })
	})
	alias := filepath.Join(t.TempDir(), "credential-alias")
	testutil.FailErr(t, "create credential alias", os.Symlink(credentials, alias))
	return []string{keys, credentials, alias}
}

func assertLaunchWriteRootRefused(t *testing.T, opts LaunchOptions, root, code string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	browser, cleanup, err := LaunchHeadless(ctx, opts)
	if browser != nil || cleanup != nil {
		t.Fatal("refused write root returned a browser or cleanup")
	}
	var refusal *confine.WriteRootRefusalError
	if !errors.As(err, &refusal) || refusal.Code != code || refusal.Path != root {
		t.Fatalf("LaunchHeadless error = %v, want typed refusal %s for %s", err, code, root)
	}
	if filepath.IsAbs(opts.CacheDir) {
		entries, readErr := os.ReadDir(opts.CacheDir)
		testutil.FailErr(t, "read cache after refusal", readErr)
		if len(entries) != 0 {
			t.Fatalf("refused launch provisioned cache or profile: %v", entries)
		}
	} else if _, statErr := os.Stat(opts.CacheDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused launch created relative cache: %v", statErr)
	}
}
