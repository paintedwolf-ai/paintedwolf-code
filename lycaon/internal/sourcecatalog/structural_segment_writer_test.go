package sourcecatalog

import (
	"bytes"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralSegmentWriterRollsSegmentsAndReadsIDs(t *testing.T) {
	writer, err := newStructuralSegmentWriterTarget(t.TempDir(), 64, 96)
	testutil.FailErr(t, "create structural segment writer", err)
	defer func() { testutil.FailErr(t, "close structural segment writer", writer.Close()) }()

	bodies := [][]byte{bytes.Repeat([]byte("f"), 45), bytes.Repeat([]byte("s"), 40), []byte("third")}
	ids := make([]uint64, 0, len(bodies))
	for _, body := range bodies {
		id, err := writer.Append(t.Context(), body)
		testutil.FailErr(t, "append writer body", err)
		ids = append(ids, id)
	}
	if ids[0]>>structuralOffsetBits == ids[1]>>structuralOffsetBits || ids[1]>>structuralOffsetBits != ids[2]>>structuralOffsetBits {
		t.Fatalf("writer did not roll expected segments: ids=%v", ids)
	}
	for i, id := range ids {
		actual, err := writer.Read(t.Context(), id)
		testutil.FailErr(t, "read writer body", err)
		if !bytes.Equal(actual, bodies[i]) {
			t.Fatalf("body %d = %q, want %q", i, actual, bodies[i])
		}
	}
	wantBytes := int64(0)
	for _, body := range bodies {
		wantBytes += structuralRecordHeaderSize + int64(len(body))
	}
	if writer.TotalBytes() != wantBytes {
		t.Fatalf("writer bytes = %d, want %d", writer.TotalBytes(), wantBytes)
	}
}

func TestStructuralSegmentWriterFreezesCandidateAndContinues(t *testing.T) {
	beforeResident := structuralResidentBytes.Load()
	beforeEncoded := structuralEncodedBytes.Load()
	writer, err := newStructuralSegmentWriterTarget(t.TempDir(), 32, 48)
	testutil.FailErr(t, "create structural segment writer", err)
	oldID, err := writer.Append(t.Context(), []byte("candidate"))
	testutil.FailErr(t, "append candidate body", err)
	candidate, err := writer.Freeze()
	testutil.FailErr(t, "freeze writer candidate", err)
	newID, err := writer.Append(t.Context(), []byte("continued"))
	testutil.FailErr(t, "append after freeze", err)
	if oldID>>structuralOffsetBits == newID>>structuralOffsetBits {
		t.Fatalf("append after freeze reused sealed segment: old=%d new=%d", oldID, newID)
	}
	oldSegment := candidate[uint32(oldID>>structuralOffsetBits)]
	oldBody, err := oldSegment.bytes.Read(t.Context(), oldID&structuralOffsetMask)
	testutil.FailErr(t, "read frozen candidate body", err)
	if string(oldBody) != "candidate" {
		t.Fatalf("frozen candidate body = %q", oldBody)
	}
	if _, found := candidate[uint32(newID>>structuralOffsetBits)]; found {
		t.Fatalf("candidate retained post-freeze segment for id %d", newID)
	}
	newBody, err := writer.Read(t.Context(), newID)
	testutil.FailErr(t, "read continued body", err)
	if string(newBody) != "continued" {
		t.Fatalf("continued body = %q", newBody)
	}
	testutil.FailErr(t, "close writer", writer.Close())
	oldBody, err = oldSegment.bytes.Read(t.Context(), oldID&structuralOffsetMask)
	testutil.FailErr(t, "read frozen candidate after writer close", err)
	if string(oldBody) != "candidate" {
		t.Fatalf("frozen candidate after close = %q", oldBody)
	}
	for _, segment := range candidate {
		segment.release()
	}
	if got := structuralResidentBytes.Load(); got != beforeResident {
		t.Fatalf("resident bytes after releasing candidate = %d, want %d", got, beforeResident)
	}
	if got := structuralEncodedBytes.Load(); got != beforeEncoded {
		t.Fatalf("encoded bytes after releasing candidate = %d, want %d", got, beforeEncoded)
	}
}

func TestStructuralSegmentWriterCloses(t *testing.T) {
	writer, err := newStructuralSegmentWriterTarget(t.TempDir(), 32, 48)
	testutil.FailErr(t, "create structural segment writer", err)
	id, err := writer.Append(t.Context(), []byte("closed"))
	testutil.FailErr(t, "append closed body", err)
	testutil.FailErr(t, "close writer", writer.Close())
	if _, err := writer.Read(t.Context(), id); !errors.Is(err, errStructuralSegmentsClosed) {
		t.Fatalf("read after close error = %v", err)
	}
}

func TestStructuralBuilderFrozenGenerationKeepsSnapshotWhileWriterContinues(t *testing.T) {
	store := checkpointTestStore(t)
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create structural builder", err)
	oldPage, err := builder.Write(t.Context(), 0, pagedview.RangePage[TreeItem]{Items: []pagedview.RangeItem[TreeItem]{{Key: DirectoryOrder("old", false), Value: TreeItem{Path: "old"}, Weight: 1}}})
	testutil.FailErr(t, "write old page", err)
	generation, err := builder.seal(t.Context(), 1)
	testutil.FailErr(t, "freeze generation", err)
	newPage, err := builder.Write(t.Context(), 0, pagedview.RangePage[TreeItem]{Items: []pagedview.RangeItem[TreeItem]{{Key: DirectoryOrder("new", false), Value: TreeItem{Path: "new"}, Weight: 1}}})
	testutil.FailErr(t, "write after freeze", err)
	if oldPage>>structuralOffsetBits == newPage>>structuralOffsetBits {
		t.Fatalf("post-freeze write reused frozen segment: old=%d new=%d", oldPage, newPage)
	}
	if _, found := generation.segments[uint32(newPage>>structuralOffsetBits)]; found {
		t.Fatal("frozen generation retained post-freeze segment")
	}
	old, err := generation.Read(t.Context(), oldPage)
	testutil.FailErr(t, "read frozen old page", err)
	if old.Items[0].Value.Path != "old" {
		t.Fatalf("frozen old page = %+v", old)
	}
	if _, err := generation.Read(t.Context(), newPage); !errors.Is(err, pagedview.ErrMissing) {
		t.Fatalf("frozen generation read post-freeze page: %v", err)
	}
	continued, err := builder.Read(t.Context(), newPage)
	testutil.FailErr(t, "read continued builder page", err)
	if continued.Items[0].Value.Path != "new" {
		t.Fatalf("continued page = %+v", continued)
	}
	builder.close()
	old, err = generation.Read(t.Context(), oldPage)
	testutil.FailErr(t, "read frozen old page after builder close", err)
	if old.Items[0].Value.Path != "old" {
		t.Fatalf("frozen old page after builder close = %+v", old)
	}
	generation.close()
}

func TestStructuralSegmentWriterSealsPreviousSegmentOnRoll(t *testing.T) {
	writer, err := newStructuralSegmentWriterTarget(t.TempDir(), 1, 32)
	testutil.FailErr(t, "create structural segment writer", err)
	defer func() { testutil.FailErr(t, "close structural segment writer", writer.Close()) }()
	firstID, err := writer.Append(t.Context(), []byte("first segment body"))
	testutil.FailErr(t, "append first body", err)
	firstSegment := writer.segments[uint32(firstID>>structuralOffsetBits)]
	if firstSegment == nil || firstSegment.bytes.spill == nil {
		t.Fatal("first segment did not spill")
	}
	_, err = writer.Append(t.Context(), []byte("second segment body"))
	testutil.FailErr(t, "append rolled body", err)
	if firstSegment.bytes.spill == nil || !firstSegment.bytes.sealed {
		t.Fatal("rolled segment dropped the only handle to its nameless spill")
	}
	actual, err := firstSegment.bytes.Read(t.Context(), firstID&structuralOffsetMask)
	testutil.FailErr(t, "read rolled segment", err)
	if string(actual) != "first segment body" {
		t.Fatalf("rolled segment body=%q", actual)
	}
}
