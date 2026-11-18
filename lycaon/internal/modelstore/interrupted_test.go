package modelstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRestartCleansUncommittedRevisionsAndAbandonedDownloads(t *testing.T) {
	s, dir, signed := bundleFixture(t)
	abandoned := filepath.Join(s.Root(), "partials", "unused")
	orphan := filepath.Join(s.Root(), "revisions", "dense", "mlx", "orphan", "model")
	for _, p := range []string{abandoned, orphan} {
		testutil.FailErr(t, "create crash directory", os.MkdirAll(filepath.Dir(p), 0700))
		testutil.FailErr(t, "create unpublished payload", os.WriteFile(p, []byte("abandoned"), 0600))
	}
	testutil.FailErr(t, "replace interrupted transfer", s.ImportFromBundlePath(t.Context(), dir, Slot{signed.Manifest.Model, signed.Manifest.Device}, nil))
	for _, p := range []string{abandoned, orphan} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("abandoned file still present: %s (%v)", p, err)
		}
	}
}

func TestPartialRefusesLinkedFile(t *testing.T) {
	s := newStore(t)
	root, err := s.openRoot()
	testutil.FailErr(t, "open root", err)
	defer func() { testutil.FailErr(t, "close root", root.Close()) }()
	original := filepath.Join(t.TempDir(), "original")
	testutil.FailErr(t, "write original", os.WriteFile(original, []byte("contents"), 0600))
	testutil.FailErr(t, "link original", os.Link(original, filepath.Join(s.Root(), "partial")))
	if f, err := openPartial(root, "partial"); err == nil {
		_ = f.Close()
		t.Fatal("accepted hardlinked partial")
	}
}
