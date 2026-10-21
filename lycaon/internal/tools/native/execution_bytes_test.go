package native

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// TestCaptureBytesPinsExactPublishedBytes: literal captures keep binary and
// non-UTF-8 bytes, refuse above their budget, and verify by recapture.
func TestCaptureBytesPinsExactPublishedBytes(t *testing.T) {
	raw := "\xff\x00bin\r\n"
	f := newPinnedDiskFixture(t, map[string]string{"blob": raw, "large": "0123456789"})
	captured, err := f.revisions.CaptureBytes(t.Context(), f.tctx, "blob", 64)
	testutil.FailErr(t, "capture binary", err)
	sum := textfile.SHA256([]byte(raw))
	want := toolcontract.ExecutionRevision{Role: toolcontract.SourceRevision, Mode: toolcontract.PublishedDiskRevision, Basis: toolcontract.CapturedRevision, RootID: "r1", Path: "blob",
		DiskSHA256: sum, Representation: toolcontract.OriginalEncodedBytes, ContentSHA256: sum, ContentBytes: int64(len(raw))}
	if string(captured.Bytes) != raw || captured.Revision != want || captured.Path != "blob" {
		t.Fatalf("captured = %+v", captured)
	}
	testutil.FailErr(t, "unchanged byte pin verifies", f.revisions.Verify(t.Context(), f.tctx, captured.Revision))
	testutil.FailErr(t, "change blob", os.WriteFile(filepath.Join(f.dir, "blob"), []byte("changed"), 0o644))
	var conflict *ExecutionRevisionConflict
	if err := f.revisions.Verify(t.Context(), f.tctx, captured.Revision); !errors.As(err, &conflict) || conflict.Current.ContentSHA256 != textfile.SHA256([]byte("changed")) {
		t.Fatalf("moved byte pin = %v", err)
	}
	_, err = f.revisions.CaptureBytes(t.Context(), f.tctx, "large", 4)
	requireRejectCode(t, err, "READ_FILE_TOO_LARGE")
	_, err = f.revisions.CaptureBytes(t.Context(), f.tctx, "missing", 4)
	requireRejectCode(t, err, "READ_PATH_NOT_FOUND")
	testutil.FailErr(t, "remove blob", os.Remove(filepath.Join(f.dir, "blob")))
	if err := f.revisions.Verify(t.Context(), f.tctx, captured.Revision); !errors.As(err, &conflict) {
		t.Fatalf("deleted byte pin = %v", err)
	}
}
