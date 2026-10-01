package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTrustSurfacesStoreRejectsUnknownSurface(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust-surfaces.yaml")
	testutil.FailErr(t, "write trust settings",
		os.WriteFile(path, []byte("enabled:\n  unknown_surface: false\n"), 0o644))
	if _, err := NewTrustSurfacesStoreAt(path); err == nil {
		t.Fatal("unknown surface was accepted")
	}
}
