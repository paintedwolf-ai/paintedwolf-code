package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/project"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The project-open deny prefixes refuse a directory as a project root; the confine
// credential-store list refuses a confined command's reads and writes. On drift, a
// store refused as a root stays readable once $HOME is attached. Confine is the
// source of truth: it is the list the sandbox enforces per action.
func TestProjectOpenPolicyCoversEveryCredentialStore(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skip("no home directory; both lists are home-relative")
	}

	// The loader expands "~/" to an absolute path, so compare in that space.
	policy := project.DefaultOpenPolicy()
	denied := make(map[string]bool, len(policy.DenyPathPrefixes))
	for _, p := range policy.DenyPathPrefixes {
		denied[filepath.Clean(strings.TrimSpace(p))] = true
	}

	for _, rel := range confine.KeyMaterialHomeRelPaths() {
		if !denied[filepath.Join(home, rel)] {
			t.Errorf("key material %q is denied to confined commands but is not a project-open deny prefix; add %q to config/packs/painted-wolf/platform/host/project-policy.yaml", rel, "~/"+rel)
		}
	}
	// Catalogued credential stores answer the same question from the pack. Root
	// attach has no ladder behind it, so a store the pack names has to be refused as
	// a project root exactly like the floor is.
	stores, err := detectionpack.BundledCredentialStorePaths()
	contractcheck.FailErr(t, "BundledCredentialStorePaths", err)
	for _, rel := range stores {
		abs := filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(rel, "~/"), "/"))
		if refused, _ := confine.AttachedWriteRootRefused(abs); !refused {
			t.Errorf("credential store %q is catalogued but attachable as a project root; nothing asks behind that door", rel)
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
