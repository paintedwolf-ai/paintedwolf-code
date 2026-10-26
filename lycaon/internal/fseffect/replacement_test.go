package fseffect

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStagedReplacementRequiresPublicationAndRevalidates(t *testing.T) {
	root := t.TempDir()
	loc := Location{Root: root, Rel: "file"}
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, "file"), []byte("before"), 0o644))
	checked := false
	staged, result, err := StageReplace(ReplaceRequest{Location: loc, Source: bytes.NewBufferString("after"), Mode: 0o644, BeforeCommit: func(Target, Result) error { checked = true; return nil }})
	testutil.FailErr(t, "stage", err)
	defer staged.Close()
	got, err := os.ReadFile(filepath.Join(root, "file"))
	testutil.FailErr(t, "read unpublished", err)
	if string(got) != "before" || result.Bytes != 5 || checked {
		t.Fatalf("staging affected destination: %q %+v checked=%v", got, result, checked)
	}
	result, err = staged.Commit()
	testutil.FailErr(t, "publish", err)
	got, err = os.ReadFile(filepath.Join(root, "file"))
	testutil.FailErr(t, "read published", err)
	if string(got) != "after" || result.Publication != PublicationVerified || !checked {
		t.Fatalf("publication = %q %+v checked=%v", got, result, checked)
	}
	if _, err := staged.Commit(); err == nil {
		t.Fatal("published replacement was accepted twice")
	}
	staged.Close()
	staged.Close()
}

func TestAbandonedReplacementRemovesOnlyStaging(t *testing.T) {
	root := t.TempDir()
	staged, _, err := StageReplace(ReplaceRequest{Location: Location{Root: root, Rel: "file"}, Source: bytes.NewBufferString("after"), Mode: 0o644})
	testutil.FailErr(t, "stage", err)
	staged.Close()
	entries, err := os.ReadDir(root)
	testutil.FailErr(t, "read directory", err)
	if len(entries) != 0 {
		t.Fatalf("abandoned entries: %v", entries)
	}
	if _, err := staged.Commit(); err == nil {
		t.Fatal("abandoned replacement was accepted")
	}
}
