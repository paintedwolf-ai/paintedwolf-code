package projectpaths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProspectiveLinkTargetResolvesLinkBeforeParentComponents(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	var err error
	outside, err = filepath.EvalSymlinks(outside)
	testutil.FailErr(t, "filepath.EvalSymlinks failed", err)
	inside := filepath.Join(outside, "inside")
	if err := os.Mkdir(inside, 0o755); err != nil {
		testutil.FailErr(t, "os.Mkdir failed", err)
	}
	if err := os.Symlink(inside, filepath.Join(root, "alias")); err != nil {
		testutil.FailErr(t, "os.Symlink failed", err)
	}
	got, err := prospectiveLinkTarget(root + string(filepath.Separator) + "alias/../missing")
	testutil.FailErr(t, "prospectiveLinkTarget failed", err)
	if want := filepath.Join(outside, "missing"); got != want {
		t.Fatalf("resolved %q, want %q", got, want)
	}
}
