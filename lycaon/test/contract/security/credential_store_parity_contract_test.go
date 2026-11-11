package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The execution-boundary write-root refusal predicate covers every key-material
// and catalogued credential store path under $HOME.
func TestWriteRootRefusalCoversEveryCredentialStore(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skip("no home directory; both lists are home-relative")
	}

	for _, rel := range confine.KeyMaterialHomeRelPaths() {
		abs := filepath.Join(home, rel)
		if refused, _ := confine.AttachedWriteRootRefused(abs); !refused {
			t.Errorf("key material %q is not refused by AttachedWriteRootRefused", rel)
		}
	}
	stores, err := detectionpack.BundledCredentialStorePaths()
	contractcheck.FailErr(t, "BundledCredentialStorePaths", err)
	for _, rel := range stores {
		abs := filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(rel, "~/"), "/"))
		if refused, _ := confine.AttachedWriteRootRefused(abs); !refused {
			t.Errorf("credential store %q is catalogued but not refused by AttachedWriteRootRefused", rel)
		}
	}
}

// Credential stores remain readable and write-denied.
func TestToolCredentialsAreWriteDeniedButReadable(t *testing.T) {
	t.Parallel()
	readDenied := make(map[string]bool)
	for _, p := range confine.SecretReadDenyRoots() {
		readDenied[p] = true
	}

	paths := confine.KeyMaterialWritePaths()
	if len(paths) == 0 {
		t.Fatal("no key-material paths resolved; the write deny would be silently empty")
	}

	writeDenied := make(map[string]bool)
	specs, err := confine.HardDenyWriteSpecs(confine.Confinement{})
	contractcheck.FailErr(t, "HardDenyWriteSpecs", err)
	for _, spec := range specs {
		if spec.Literal != "" {
			writeDenied[spec.Literal] = true
		}
		if spec.Subpath != "" {
			writeDenied[spec.Subpath] = true
		}
	}

	for _, p := range paths {
		if !writeDenied[p] {
			t.Errorf("key material %q is not write-denied; what it selects to run would survive the session", p)
		}
		if readDenied[p] {
			t.Errorf("key material %q is read-denied; that breaks the CLI it belongs to, which the approval layer — not the read boundary — is meant to govern", p)
		}
	}
}

// The key-material floor is pack data, not a Go list: confine holds the seam,
// the shipped pack holds the names, and clearing the seam leaves nothing behind.
func TestKeyMaterialFloorIsPackDerived(t *testing.T) {
	catalogued, err := detectionpack.BundledKeyMaterialPaths()
	contractcheck.FailErr(t, "BundledKeyMaterialPaths", err)
	if len(catalogued) == 0 {
		t.Fatal("the shipped key-material pack names no paths; the floor would be empty")
	}
	for _, p := range catalogued {
		if !strings.HasPrefix(p, "~/") {
			t.Errorf("key-material path %q is not home-relative; the pack is shared across machines", p)
		}
	}

	// Not parallel and restored below: the source is process-wide, and the check
	// is that nothing answers when it is absent.
	restore := func() []string { return catalogued }
	confine.SetKeyMaterialPathsSource(nil)
	residue := confine.KeyMaterialHomeRelPaths()
	confine.SetKeyMaterialPathsSource(restore)
	if len(residue) != 0 {
		t.Fatalf("confine answers %v with no catalogue installed; the list must come from the catalogue", residue)
	}

	installed := confine.KeyMaterialHomeRelPaths()
	if len(installed) != len(catalogued) {
		t.Fatalf("installed floor = %v, catalogue = %v", installed, catalogued)
	}
	for _, rel := range installed {
		if strings.HasPrefix(rel, "~") || strings.HasSuffix(rel, "/") {
			t.Errorf("home-relative floor entry %q still carries catalogue syntax", rel)
		}
	}
}
