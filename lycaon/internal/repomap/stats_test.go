package repomap_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBuildStatsSingleFile(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "one.txt"), []byte("a\nb\n"), 0o644))

	snap, err := repomap.BuildStats(context.Background(), repomap.StatsOptions{
		Root:    dir,
		Subpath: "one.txt",
	})
	testutil.FailErr(t, "BuildStats", err)
	if snap.Mode != "stats" || snap.Files != 1 || snap.TextLines != 2 {
		t.Fatalf("snap = %+v", snap)
	}
}

func TestBuildStatsDecodesSelfIdentifyingEncodings(t *testing.T) {
	for _, encoding := range testutil.SelfIdentifyingTextEncodings() {
		t.Run(encoding, func(t *testing.T) {
			dir := t.TempDir()
			raw := testutil.EncodeTextFixture(t, "a\nb\n", encoding)
			testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, "document.txt"), raw, 0o644))
			snapshot, err := repomap.BuildStats(context.Background(), repomap.StatsOptions{
				Root: dir, Subpath: "document.txt",
			})
			testutil.FailErr(t, "build stats", err)
			if snapshot.TextLines != 2 || len(snapshot.LargestText) != 1 || !snapshot.LargestText[0].IsText {
				t.Fatalf("snapshot = %+v", snapshot)
			}
		})
	}
}
