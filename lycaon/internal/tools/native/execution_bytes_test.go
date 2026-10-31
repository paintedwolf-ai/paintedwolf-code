package native

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
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
	requireRejectCode(t, err, "COMMAND_LIMIT_EXCEEDED")
	_, err = f.revisions.CaptureBytes(t.Context(), f.tctx, "missing", 4)
	requireRejectCode(t, err, "READ_PATH_NOT_FOUND")
	testutil.FailErr(t, "remove blob", os.Remove(filepath.Join(f.dir, "blob")))
	if err := f.revisions.Verify(t.Context(), f.tctx, captured.Revision); !errors.As(err, &conflict) {
		t.Fatalf("deleted byte pin = %v", err)
	}
}

// TestPinOpenedNamesTheOpenedObject: a pin taken through another owner's
// open descriptor hashes that object without moving its offset.
func TestPinOpenedNamesTheOpenedObject(t *testing.T) {
	f := newPinnedDiskFixture(t, map[string]string{"seen.txt": "seen bytes\n"})
	resolved, err := projectpaths.ResolveRead(t.Context(), f.revisions.Boundary, f.tctx, "seen.txt")
	testutil.FailErr(t, "resolve", err)
	file, err := os.Open(resolved.Abs)
	testutil.FailErr(t, "open", err)
	defer file.Close()
	revision, ok, err := f.revisions.PinOpened(t.Context(), resolved, file)
	testutil.FailErr(t, "pin opened", err)
	sum := textfile.SHA256([]byte("seen bytes\n"))
	if !ok || revision.Path != "seen.txt" || revision.DiskSHA256 != sum || revision.ContentSHA256 != sum || revision.ContentBytes != 11 {
		t.Fatalf("revision = %+v", revision)
	}
	rest, err := io.ReadAll(file)
	if err != nil || string(rest) != "seen bytes\n" {
		t.Fatalf("pin moved the owner's offset: %q %v", rest, err)
	}
	dir, err := os.Open(f.dir)
	testutil.FailErr(t, "open dir", err)
	defer dir.Close()
	if _, ok, err := f.revisions.PinOpened(t.Context(), resolved, dir); ok || err != nil {
		t.Fatalf("directory pinned: %v %v", ok, err)
	}
}
