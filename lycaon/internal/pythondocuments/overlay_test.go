package pythondocuments

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
)

func readAllGuest(t *testing.T, f pythonruntime.GuestFile) []byte {
	t.Helper()
	var out []byte
	buf := make([]byte, 64)
	for {
		n, errno := f.Read(buf)
		if errno != pythonruntime.ErrnoSuccess {
			t.Fatalf("GuestFile.Read failed: %s", errno)
		}
		if n == 0 {
			return out
		}
		out = append(out, buf[:n]...)
	}
}

func TestUnsavedDraftVsSavedDiskAndEvidence(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	store.SetSaved("root-main", "", "notes.txt", []byte("disk"), 0o644)
	store.SetUnsavedDraft("root-main", "", "notes.txt", []byte("draft"))

	evidence := NewEvidenceLog()
	adapter := NewOverlayAdapter(OverlayConfig{
		ExecutionID: "exec-draft",
		Store:       store,
		Roots:       []RootSpec{{ID: "root-main", Label: "main", Default: true}},
		Evidence:    evidence,
	})

	f, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "notes.txt",
		Flags: pythonruntime.OpenRead,
		OriginFrame: &pythonruntime.DeclaredFrame{
			File:       "script.py",
			Line:       10,
			Function:   "main",
			Confidence: pythonruntime.FrameDeclared,
		},
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open notes.txt failed: %s", errno)
	}
	got := readAllGuest(t, f)
	if string(got) != "draft" {
		t.Fatalf("guest read %q, want %q", string(got), "draft")
	}
	if errno := f.Close(); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Close failed: %s", errno)
	}

	// Saved disk must still hold "disk".
	saved, ok := store.SavedBytes("root-main", "", "notes.txt")
	if !ok || string(saved) != "disk" {
		t.Fatalf("saved disk = %q (ok=%v), want %q", string(saved), ok, "disk")
	}

	// Verify PY05 DocumentReadRecord shape and JSON round-trip.
	reads := evidence.Reads()
	if len(reads) != 1 {
		t.Fatalf("recorded %d DocumentReadRecords, want 1", len(reads))
	}
	rec := reads[0]
	if rec.ExecutionID != "exec-draft" || !rec.UnsavedDraft || rec.BytesDeliveredToGuest != 5 {
		t.Fatalf("unexpected DocumentReadRecord: %+v", rec)
	}
	rawJSON, err := json.Marshal(rec)
	testutil.FailErr(t, "marshal DocumentReadRecord", err)
	var decoded DocumentReadRecord
	testutil.FailErr(t, "unmarshal DocumentReadRecord", json.Unmarshal(rawJSON, &decoded))
	if decoded.Target.RelPath != "notes.txt" || !decoded.UnsavedDraft || decoded.Revision.DocumentRevision == "" {
		t.Fatalf("decoded DocumentReadRecord mismatch: %+v", decoded)
	}
}

func TestBufferedWriteReopenFlushCloseAndReaderOffsets(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	store.SetSaved("root-main", "", "doc.txt", []byte("disk"), 0o644)
	store.SetUnsavedDraft("root-main", "", "doc.txt", []byte("draft"))

	adapter := NewOverlayAdapter(OverlayConfig{
		ExecutionID: "exec-buf",
		Store:       store,
		Roots:       []RootSpec{{ID: "root-main", Label: "main", Default: true}},
	})

	// Existing reader opened before write keeps its own offset and snapshot.
	earlyReader, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "doc.txt",
		Flags: pythonruntime.OpenRead,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open earlyReader failed: %s", errno)
	}
	head := make([]byte, 2)
	n, errno := earlyReader.Read(head)
	if errno != pythonruntime.ErrnoSuccess || n != 2 || string(head) != "dr" {
		t.Fatalf("earlyReader read (%q, %s), want (\"dr\", SUCCESS)", string(head), errno)
	}

	// Open writer with truncate.
	writer, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "doc.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate | pythonruntime.OpenCreate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open writer failed: %s", errno)
	}

	// Newly opened reader sees empty file immediately after open+truncate.
	r1, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "doc.txt", Flags: pythonruntime.OpenRead})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open r1 failed: %s", errno)
	}
	if got := readAllGuest(t, r1); len(got) != 0 {
		t.Fatalf("r1 after truncate saw %q, want empty", string(got))
	}

	// Write "prefix" and sync (guest staging flush).
	if nw, errno := writer.Write([]byte("prefix")); errno != pythonruntime.ErrnoSuccess || nw != 6 {
		t.Fatalf("writer.Write = (%d, %s), want (6, SUCCESS)", nw, errno)
	}
	if errno := writer.Sync(); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("writer.Sync failed: %s", errno)
	}

	// Newly opened reader sees "prefix"; r1 at offset 0 also sees "prefix";
	// earlyReader continues from offset 2 of its snapshot ("aft").
	r2, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "doc.txt", Flags: pythonruntime.OpenRead})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open r2 failed: %s", errno)
	}
	if got := readAllGuest(t, r2); string(got) != "prefix" {
		t.Fatalf("r2 after write saw %q, want %q", string(got), "prefix")
	}
	if got := readAllGuest(t, r1); string(got) != "prefix" {
		t.Fatalf("r1 after write saw %q, want %q", string(got), "prefix")
	}
	if got := readAllGuest(t, earlyReader); string(got) != "aft" {
		t.Fatalf("earlyReader tail saw %q, want %q", string(got), "aft")
	}

	// Close writer: host store MUST still hold original draft/disk until Settle!
	_ = writer.Close()
	_ = r1.Close()
	_ = r2.Close()
	_ = earlyReader.Close()

	if eff, _ := store.EffectiveBytes("root-main", "", "doc.txt"); string(eff) != "draft" {
		t.Fatalf("host draft mutated before Settle: got %q, want %q", string(eff), "draft")
	}
	if saved, _ := store.SavedBytes("root-main", "", "doc.txt"); string(saved) != "disk" {
		t.Fatalf("host disk mutated before Settle: got %q, want %q", string(saved), "disk")
	}

	// Clean-success settlement publishes "prefix" to host store.
	report, err := adapter.Settle(ctx, true)
	testutil.FailErr(t, "Settle clean", err)
	if len(report.Published) != 1 || report.Published[0].BytesWritten != 6 {
		t.Fatalf("unexpected settlement report: %+v", report)
	}
	if saved, _ := store.SavedBytes("root-main", "", "doc.txt"); string(saved) != "prefix" {
		t.Fatalf("host disk after Settle = %q, want %q", string(saved), "prefix")
	}
}

func TestBOMCRLFAndBinaryFidelity(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	orig := append([]byte{0xEF, 0xBB, 0xBF}, []byte("line1\r\nline2\r\n")...)
	binData := []byte{0x00, 0x01, 0x7F, 0x80, 0xFE, 0xFF, 0x00, 0x0D, 0x0A}
	store.SetSaved("r1", "", "bom.txt", orig, 0o644)
	store.SetSaved("r1", "", "blob.bin", binData, 0o644)

	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{{ID: "r1", Label: "main", Default: true}},
	})

	f1, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "bom.txt", Flags: pythonruntime.OpenRead})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open bom.txt: %s", errno)
	}
	gotBOM := readAllGuest(t, f1)
	_ = f1.Close()
	if !bytes.Equal(gotBOM, orig) {
		t.Fatalf("BOM+CRLF mismatch: got %x, want %x", gotBOM, orig)
	}

	f2, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "blob.bin", Flags: pythonruntime.OpenRead | pythonruntime.OpenWrite})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open blob.bin: %s", errno)
	}
	gotBin := readAllGuest(t, f2)
	if !bytes.Equal(gotBin, binData) {
		t.Fatalf("binary read mismatch: got %x, want %x", gotBin, binData)
	}
	if pos, errno := f2.Seek(0, io.SeekEnd); errno != pythonruntime.ErrnoSuccess || pos != int64(len(binData)) {
		t.Fatalf("Seek end = (%d, %s)", pos, errno)
	}
	extra := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	if _, errno := f2.Write(extra); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Write extra binary: %s", errno)
	}
	_ = f2.Close()

	_, err := adapter.Settle(ctx, true)
	testutil.FailErr(t, "Settle binary", err)
	wantBin := append(append([]byte(nil), binData...), extra...)
	savedBin, ok := store.SavedBytes("r1", "", "blob.bin")
	if !ok || !bytes.Equal(savedBin, wantBin) {
		t.Fatalf("published binary mismatch: got %x, want %x", savedBin, wantBin)
	}
}

func TestCrossMountEXDEVAndDirectoryOverlaySemantics(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	store.SetSaved("root-a", "", "a.txt", []byte("A"), 0o644)
	store.SetSaved("root-b", "", "b.txt", []byte("B"), 0o644)

	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{
			{ID: "root-a", Label: "alpha", Default: true},
			{ID: "root-b", Label: "beta"},
		},
		ReadOnlyFiles: map[CanonicalKey][]byte{
			{Namespace: pythonruntime.NamespaceRuntime, RootID: "runtime", RelPath: "stdlib.py"}: []byte("# stdlib"),
		},
	})

	// Create scratch file and attempt cross-mount rename into workspace -> EXDEV.
	sf, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "/scratch/temp-1234",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenCreate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open scratch file: %s", errno)
	}
	_, _ = sf.Write([]byte("scratch"))
	_ = sf.Close()

	if errno := adapter.Rename(ctx, "/scratch/temp-1234", "a.txt"); errno != pythonruntime.ErrnoEXDEV {
		t.Fatalf("scratch->workspace Rename = %s, want EXDEV", errno)
	}
	if errno := adapter.Rename(ctx, "@alpha/a.txt", "@beta/a.txt"); errno != pythonruntime.ErrnoEXDEV {
		t.Fatalf("cross-root Rename = %s, want EXDEV", errno)
	}

	// Read-only runtime namespace refuses writes with EROFS.
	if _, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "/runtime/stdlib.py",
		Flags: pythonruntime.OpenWrite,
	}); errno != pythonruntime.ErrnoEROFS {
		t.Fatalf("write to /runtime = %s, want EROFS", errno)
	}

	// /dev/null accepts writes and returns EOF on read.
	nf, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "/dev/null",
		Flags: pythonruntime.OpenRead | pythonruntime.OpenWrite,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open /dev/null: %s", errno)
	}
	if n, errno := nf.Write([]byte("discarded")); errno != pythonruntime.ErrnoSuccess || n != 9 {
		t.Fatalf("Write /dev/null = (%d, %s)", n, errno)
	}
	if got := readAllGuest(t, nf); len(got) != 0 {
		t.Fatalf("Read /dev/null = %q, want empty", string(got))
	}
	_ = nf.Close()

	// Mkdir, Create, ReadDir, Unlink, Rmdir visibility in overlay.
	if errno := adapter.Mkdir(ctx, "sub", 0o755); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Mkdir sub: %s", errno)
	}
	cf, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "sub/new.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenCreate | pythonruntime.OpenExclusive,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("OpenCreate|Exclusive sub/new.txt: %s", errno)
	}
	_, _ = cf.Write([]byte("hello"))
	_ = cf.Close()

	// Duplicate OpenCreate|OpenExclusive returns EEXIST.
	if _, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "sub/new.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenCreate | pythonruntime.OpenExclusive,
	}); errno != pythonruntime.ErrnoEEXIST {
		t.Fatalf("duplicate exclusive create = %s, want EEXIST", errno)
	}

	// Non-empty Rmdir fails with EEXIST.
	if errno := adapter.Rmdir(ctx, "sub"); errno != pythonruntime.ErrnoEEXIST {
		t.Fatalf("non-empty Rmdir = %s, want EEXIST", errno)
	}

	entries, errno := adapter.ReadDir(ctx, "sub")
	if errno != pythonruntime.ErrnoSuccess || len(entries) != 1 || entries[0].Name != "new.txt" {
		t.Fatalf("ReadDir sub = (%+v, %s), want [new.txt]", entries, errno)
	}

	if errno := adapter.Unlink(ctx, "sub/new.txt"); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Unlink sub/new.txt: %s", errno)
	}
	if errno := adapter.Rmdir(ctx, "sub"); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Rmdir sub: %s", errno)
	}
}

func TestOpenAppendTruncateSeekAndClosedHandleErrors(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	store.SetSaved("r1", "", "log.txt", []byte("one\n"), 0o644)

	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{{ID: "r1", Label: "main", Default: true}},
	})

	f, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "log.txt",
		Flags: pythonruntime.OpenRead | pythonruntime.OpenWrite | pythonruntime.OpenAppend,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open append: %s", errno)
	}
	if _, errno := f.Write([]byte("two\n")); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Write append: %s", errno)
	}
	if _, errno := f.Seek(0, io.SeekStart); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Seek start: %s", errno)
	}
	if got := readAllGuest(t, f); string(got) != "one\ntwo\n" {
		t.Fatalf("appended content = %q, want %q", string(got), "one\ntwo\n")
	}
	if errno := f.Truncate(3); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Truncate(3): %s", errno)
	}
	if _, errno := f.Seek(0, io.SeekStart); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Seek start after truncate: %s", errno)
	}
	if got := readAllGuest(t, f); string(got) != "one" {
		t.Fatalf("truncated content = %q, want %q", string(got), "one")
	}
	if errno := f.Close(); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Close: %s", errno)
	}
	if _, errno := f.Read(make([]byte, 4)); errno != pythonruntime.ErrnoEBADF {
		t.Fatalf("Read after Close = %s, want EBADF", errno)
	}
	if _, errno := f.Write([]byte("x")); errno != pythonruntime.ErrnoEBADF {
		t.Fatalf("Write after Close = %s, want EBADF", errno)
	}
}

func TestBytesDeliveredToGuestSemanticsAndParentOperationID(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	store.SetSaved("root-main", "", "secret.txt", []byte("0123456789abcdef"), 0o644)

	evidence := NewEvidenceLog()
	adapter := NewOverlayAdapter(OverlayConfig{
		ExecutionID: "exec-bytes",
		Store:       store,
		Roots:       []RootSpec{{ID: "root-main", Label: "main", Default: true}},
		Evidence:    evidence,
	})

	// (a) Defective-control regression: Open + Close with zero Read calls MUST deliver 0 bytes.
	f0, errno := adapter.Open(WithParentOperationID(ctx, "sysfs-op-open-close"), pythonruntime.OpenRequest{
		Path:  "secret.txt",
		Flags: pythonruntime.OpenRead,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open(a): %s", errno)
	}
	if errno := f0.Close(); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Close(a): %s", errno)
	}

	// (b) Defective-control regression: Open + 1-byte Read MUST deliver 1 byte (never len(snap.Bytes)=16).
	f1, errno := adapter.Open(WithParentOperationID(ctx, "sysfs-op-one-byte"), pythonruntime.OpenRequest{
		Path:  "secret.txt",
		Flags: pythonruntime.OpenRead,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open(b): %s", errno)
	}
	one := make([]byte, 1)
	n, errno := f1.Read(one)
	if errno != pythonruntime.ErrnoSuccess || n != 1 || string(one) != "0" {
		t.Fatalf("1-byte Read = (%d, %q, %s), want (1, \"0\", SUCCESS)", n, string(one), errno)
	}
	_ = f1.Close()

	// (c) Non-contiguous seek + read records exact delivered bytes and distinct ranges.
	f2, errno := adapter.Open(WithParentOperationID(ctx, "sysfs-op-ranges"), pythonruntime.OpenRequest{
		Path:  "secret.txt",
		Flags: pythonruntime.OpenRead,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open(c): %s", errno)
	}
	two := make([]byte, 2)
	_, _ = f2.Read(two) // [0,2)
	_, _ = f2.Seek(10, io.SeekStart)
	three := make([]byte, 3)
	_, _ = f2.Read(three) // [10,13)
	_ = f2.Close()

	reads := evidence.Reads()
	if len(reads) != 3 {
		t.Fatalf("recorded %d DocumentReadRecords, want 3", len(reads))
	}
	if reads[0].BytesDeliveredToGuest != 0 || len(reads[0].DeliveredRanges) != 0 || reads[0].ParentOperationID != "sysfs-op-open-close" {
		t.Fatalf("open+close read record = %+v, want 0 bytes and parent sysfs-op-open-close", reads[0])
	}
	if reads[1].BytesDeliveredToGuest != 1 || len(reads[1].DeliveredRanges) != 1 || reads[1].DeliveredRanges[0] != (ByteRange{Start: 0, End: 1}) || reads[1].ParentOperationID != "sysfs-op-one-byte" {
		t.Fatalf("1-byte read record = %+v, want 1 byte [0,1) and parent sysfs-op-one-byte", reads[1])
	}
	if reads[2].BytesDeliveredToGuest != 5 || len(reads[2].DeliveredRanges) != 2 || reads[2].DeliveredRanges[0] != (ByteRange{Start: 0, End: 2}) || reads[2].DeliveredRanges[1] != (ByteRange{Start: 10, End: 13}) {
		t.Fatalf("ranged read record = %+v, want 5 bytes [[0,2),[10,13)]", reads[2])
	}

	// Verify ParentOperationID propagation onto OverlayEventRecord and PublicationRecord.
	w, errno := adapter.Open(WithParentOperationID(ctx, "sysfs-op-write"), pythonruntime.OpenRequest{
		Path:  "secret.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open write: %s", errno)
	}
	_, _ = w.Write([]byte("updated"))
	_ = w.Close()

	_, err := adapter.Settle(WithParentOperationID(ctx, "sysfs-op-settle"), true)
	testutil.FailErr(t, "Settle with parent op", err)

	events := evidence.OverlayEvents()
	if len(events) == 0 || events[0].ParentOperationID != "sysfs-op-write" {
		t.Fatalf("overlay events missing ParentOperationID: %+v", events)
	}
	pubs := evidence.Publications()
	if len(pubs) != 1 || pubs[0].ParentOperationID != "sysfs-op-settle" {
		t.Fatalf("publication record missing ParentOperationID: %+v", pubs)
	}
}
