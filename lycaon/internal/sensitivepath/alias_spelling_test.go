package sensitivepath_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestEverySpellingOfACataloguedLocationClassifiesTheSame checks path aliases.
func TestEverySpellingOfACataloguedLocationClassifiesTheSame(t *testing.T) {
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "home dir", err)
	cat, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "load bundled catalog", err)

	checked := 0
	for _, loc := range cat.Locations() {
		for _, raw := range loc.Paths {
			target := raw
			if strings.HasPrefix(raw, "~/") {
				target = filepath.Join(home, raw[2:])
			}
			if !filepath.IsAbs(target) {
				continue
			}
			mode := sensitivepath.ModeRead
			if loc.Mode == sensitivepath.ModeWrite {
				mode = sensitivepath.ModeWrite
			}
			want, ok := cat.ClassifyResolved(target, mode)
			if !ok {
				continue // a location this host does not have
			}
			for name, alias := range testutil.AliasSpellings(t, target) {
				checked++
				got, ok := cat.ClassifyResolved(alias, mode)
				if !ok {
					t.Errorf("%s (%s): %q classifies as %q but %q classifies as nothing",
						loc.ID, name, target, want.ID, alias)
					continue
				}
				if got.ID != want.ID {
					t.Errorf("%s (%s): %q -> %q but %q -> %q",
						loc.ID, name, target, want.ID, alias, got.ID)
				}
			}
		}
	}
	t.Logf("checked %d alias spellings of catalogued locations", checked)
	if checked == 0 {
		t.Skip("this host offers no alias spellings of any catalogued location")
	}
}

// TestCatalogPathsAreComparedCanonically matches aliased catalog paths.
func TestCatalogPathsAreComparedCanonically(t *testing.T) {
	if fi, err := os.Lstat("/etc"); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Skip("/etc is not a symlink here")
	}
	cat, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "load bundled catalog", err)

	for _, rel := range []string{"ssh/ssh_host_rsa_key", "sudoers", "hosts", "crontab", "profile"} {
		short := filepath.Join("/etc", rel)
		long := filepath.Join("/private/etc", rel)
		for _, mode := range []sensitivepath.Mode{sensitivepath.ModeRead, sensitivepath.ModeWrite} {
			a, okA := cat.ClassifyResolved(short, mode)
			b, okB := cat.ClassifyResolved(long, mode)
			if okA != okB || a.ID != b.ID {
				t.Errorf("(%s) %s -> %v/%q but %s -> %v/%q — one directory, two verdicts",
					mode, short, okA, a.ID, long, okB, b.ID)
			}
		}
	}
}

// TestHardlinkAliasCarriesNoClassification verifies hardlinks lack path identity.
func TestHardlinkAliasCarriesNoClassification(t *testing.T) {
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "home dir", err)
	src := filepath.Join(home, "Documents", ".sensitivepath-hardlink-probe")
	if err := os.WriteFile(src, []byte("probe\n"), 0o600); err != nil {
		t.Skipf("cannot write under ~/Documents: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(src) })

	cat, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "load bundled catalog", err)
	if _, ok := cat.ClassifyResolved(src, sensitivepath.ModeRead); !ok {
		t.Skip("~/Documents is not catalogued on this host")
	}

	hard := filepath.Join(t.TempDir(), "hard-probe")
	if err := os.Link(src, hard); err != nil {
		t.Skipf("cannot hardlink: %v", err)
	}
	if _, ok := cat.ClassifyResolved(hard, sensitivepath.ModeRead); ok {
		t.Fatal("a hardlink now classifies — update this test and the catalog header, " +
			"which both record that path-based classification cannot see one")
	}
}

// TestCatalogRefusesAnUnknownFormatVersion enforces the catalog version.
func TestCatalogRefusesAnUnknownFormatVersion(t *testing.T) {
	dir := t.TempDir()
	body := "version: 99\nlocations:\n  - id: x\n    title: X\n    mode: read\n    paths: [\"/tmp/x\"]\n"
	testutil.FailErr(t, "write overlay", os.WriteFile(filepath.Join(dir, "over.yaml"), []byte(body), 0o644))
	if _, err := sensitivepath.Load(sensitivepath.Dir(dir)); err == nil {
		t.Fatal("an overlay declaring an unknown catalog version loaded instead of being refused")
	}
}
