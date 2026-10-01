package localdata

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func seedPreImage(t *testing.T, base, id string, body string) string {
	t.Helper()
	dir := filepath.Join(base, RestorePreImageDirPrefix+"-"+id)
	testutil.FailErr(t, "seed pre-image dir", os.MkdirAll(dir, 0o700))
	testutil.FailErr(t, "seed pre-image file",
		os.WriteFile(filepath.Join(dir, "store.db"), []byte(body), 0o600))
	return dir
}

func TestRestorePreImagesAreNamedAndMeasured(t *testing.T) {
	base := t.TempDir()
	seedPreImage(t, base, "11111111-1111-1111-1111-111111111111", "aaaa")
	seedPreImage(t, base, "22222222-2222-2222-2222-222222222222", "bb")
	// Neither the staging tree nor an unrelated dot-directory is a pre-image.
	testutil.FailErr(t, "seed staging",
		os.MkdirAll(filepath.Join(base, RestoreStagingDirPrefix+"-33333333-3333-3333-3333-333333333333"), 0o700))
	testutil.FailErr(t, "seed unrelated", os.MkdirAll(filepath.Join(base, ".cache"), 0o700))

	images, err := RestorePreImages(base)
	testutil.FailErr(t, "list pre-images", err)
	if len(images) != 2 {
		t.Fatalf("pre-images = %d, want 2 (%v)", len(images), images)
	}

	total, err := RestorePreImageBytes(base)
	testutil.FailErr(t, "measure pre-images", err)
	if total != 6 {
		t.Fatalf("pre-image bytes = %d, want 6", total)
	}
}

func TestPruneRestorePreImagesKeepsOnlyTheNamedTransactions(t *testing.T) {
	base := t.TempDir()
	stale := seedPreImage(t, base, "11111111-1111-1111-1111-111111111111", "old")
	keep := seedPreImage(t, base, "22222222-2222-2222-2222-222222222222", "new")
	failed := seedPreImage(t, base, "33333333-3333-3333-3333-333333333333", "half-applied")

	alias := filepath.Join(t.TempDir(), "config")
	testutil.FailErr(t, "alias configuration directory", os.Symlink(base, alias))
	removed, err := PruneRestorePreImages(base, keep, filepath.Join(alias, filepath.Base(failed)))
	testutil.FailErr(t, "prune pre-images", err)
	if len(removed) != 1 || removed[0].Path != stale {
		t.Fatalf("removed = %v, want only %s", removed, stale)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("prune removed the pre-image of the pending transaction")
	}
	if _, err := os.Stat(failed); err != nil {
		t.Fatal("prune removed the pre-image a failed transaction displaced")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("prune left a pre-image no transaction still needs")
	}
}
