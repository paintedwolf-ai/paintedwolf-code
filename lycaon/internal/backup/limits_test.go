package backup

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestArchiveWriterRefusesBeyondLimitWithoutWritingExcess(t *testing.T) {
	var dest bytes.Buffer
	w := &archiveLimitWriter{dest: &dest, remaining: 8}
	_, err := w.Write([]byte("12345678"))
	testutil.FailErr(t, "write at limit", err)
	if n, err := w.Write([]byte("9")); n != 0 || err == nil {
		t.Fatalf("excess write=(%d,%v)", n, err)
	}
	if dest.String() != "12345678" {
		t.Fatalf("limited bytes=%q", dest.String())
	}
}

func TestCaptureLimitsIncludeManifestAndMatchRestoreLimits(t *testing.T) {
	manifest := Manifest{Files: []FileEntry{{Kind: fileKindRegular, Size: int64(MaxExpandedArchiveBytes) - 2}}}
	testutil.FailErr(t, "expanded exactly at limit", validateCaptureManifest(manifest, []byte("{}")))
	manifest.Files[0].Size++
	if err := validateCaptureManifest(manifest, []byte("{}")); err == nil {
		t.Fatal("expanded archive omitted manifest bytes")
	}
	manifest.Files[0] = FileEntry{Kind: fileKindSymlink, Size: maxSymlinkTargetBytes + 1}
	if err := validateCaptureManifest(manifest, []byte("{}")); err == nil {
		t.Fatal("oversized symlink was exportable but not restorable")
	}
}

func TestCanceledCaptureStopsWithinCurrentFile(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := contextReader{ctx: ctx, in: bytes.NewBufferString("body")}
	if n, err := r.Read(make([]byte, 4)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read=(%d,%v)", n, err)
	}
}

func TestUpgradeSpaceRequiresRecoveryAndScratchAllowance(t *testing.T) {
	if err := requireUpgradeSpace(4<<30, 3<<30); err == nil {
		t.Fatal("insufficient space admitted upgrade")
	}
	testutil.FailErr(t, "space exactly meets estimate", requireUpgradeSpace(4<<30, 4<<30))
}

func TestWorkerBackupScopeIncludesOnlyLayoutMetadata(t *testing.T) {
	const root = "worker-branches/0123456789abcdef"
	for _, path := range []string{root + "/job-1.meta/state.json", root + "/a.meta/state.json"} {
		if !RestorableRelPath(path) {
			t.Fatalf("layout metadata excluded: %s", path)
		}
	}
	for _, path := range []string{root + "/job-1/src/main.go", root + "/job-1.meta/used-at", root + "/job-1.meta/sub/state.json", "worker-branches/not-a-key/job.meta/state.json", "./store.db"} {
		if RestorableRelPath(path) {
			t.Fatalf("non-durable path included: %s", path)
		}
	}
	if workerMetadataPath(root+"/job-1", true) {
		t.Fatal("capture would descend into a rebuildable branch tree")
	}
}
