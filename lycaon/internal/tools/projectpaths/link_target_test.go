package projectpaths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProspectiveLinkTargetResolvesLinkBeforeParentComponents(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	var err error
	outside, err = filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(outside, "inside")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	got, err := prospectiveLinkTarget(root + string(filepath.Separator) + "alias/../missing")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(outside, "missing"); got != want {
		t.Fatalf("resolved %q, want %q", got, want)
	}
}
