package sensitivepath_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Match catalog paths with the filesystem's case-folding behavior.
func TestMatchFoldsCaseWhereTheFilesystemDoes(t *testing.T) {
	h, err := os.UserHomeDir()
	testutil.FailErr(t, "home dir", err)
	if !confine.FoldsCase(h) {
		t.Skip("case-sensitive filesystem: ~/.SSH is a different directory here")
	}
	cat, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "load bundled catalog", err)

	exact := filepath.Join(h, ".ssh", "id_ed25519")
	want, ok := cat.Match(exact, sensitivepath.ModeRead)
	if !ok {
		t.Fatalf("bundled catalog does not cover %s", exact)
	}
	variant := filepath.Join(h, ".SSH", "id_ed25519")
	got, ok := cat.Match(variant, sensitivepath.ModeRead)
	if !ok || got.ID != want.ID {
		t.Fatalf("Match(%q) = %+v/%v, want id %s", variant, got, ok, want.ID)
	}
}

// The basename globs are the half that catches key material in directories
// nobody listed, so they fold too.
func TestNameGlobFoldsCaseWhereTheFilesystemDoes(t *testing.T) {
	h, err := os.UserHomeDir()
	testutil.FailErr(t, "home dir", err)
	if !confine.FoldsCase(h) {
		t.Skip("case-sensitive filesystem: ID_RSA is a different file here")
	}
	cat, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "load bundled catalog", err)

	lower := filepath.Join(h, "projects", "id_rsa")
	want, ok := cat.Match(lower, sensitivepath.ModeRead)
	if !ok {
		t.Fatalf("bundled catalog does not name-match %s", lower)
	}
	upper := filepath.Join(h, "projects", "ID_RSA")
	got, ok := cat.Match(upper, sensitivepath.ModeRead)
	if !ok || got.ID != want.ID {
		t.Fatalf("Match(%q) = %+v/%v, want id %s", upper, got, ok, want.ID)
	}
}
