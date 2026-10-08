package sourcecatalog

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralSegmentsBufferedSpillRoundTrip(t *testing.T) {
	store, err := newStructuralSegments(t.TempDir(), 128)
	testutil.FailErr(t, "create buffered segments", err)
	defer func() { testutil.FailErr(t, "close buffered segments", store.Close()) }()
	want := make(map[uint64][]byte)
	for i, length := range []int{80, 80, 40, 256, 20} {
		body := bytes.Repeat([]byte{byte(i)}, length)
		id, err := store.Append(t.Context(), body)
		testutil.FailErr(t, "append buffered record", err)
		want[id] = body
	}
	if store.spill == nil || len(store.spillBuffer) == 0 {
		t.Fatal("fixture did not retain a buffered spill tail")
	}
	info, err := store.spill.Stat()
	testutil.FailErr(t, "inspect flushed spill prefix", err)
	if info.Size() != store.spillOffset || info.Size() >= store.Bytes() {
		t.Fatalf("spill prefix = %d, offset = %d, complete bytes = %d", info.Size(), store.spillOffset, store.Bytes())
	}
	testutil.FailErr(t, "seal without flushing buffered tail", store.Seal())
	for id, expected := range want {
		actual, err := store.Read(t.Context(), id)
		testutil.FailErr(t, "read buffered record", err)
		if !bytes.Equal(actual, expected) {
			t.Fatalf("record %d changed across buffered spill", id)
		}
	}
}

func TestStructuralSegmentsAppendReadAndSeal(t *testing.T) {
	store, err := newStructuralSegments(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatalf("create structural segments: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	one := []byte("one")
	oneID, err := store.Append(t.Context(), one)
	if err != nil {
		t.Fatalf("append first segment: %v", err)
	}
	one[0] = 'x'
	twoID, err := store.Append(t.Context(), []byte("second"))
	if err != nil {
		t.Fatalf("append second segment: %v", err)
	}
	if oneID == 0 || twoID <= oneID || twoID >= 1<<40 {
		t.Fatalf("unexpected segment ids: first=%d second=%d", oneID, twoID)
	}
	got, err := store.Read(t.Context(), oneID)
	if err != nil {
		t.Fatalf("read first segment: %v", err)
	}
	if string(got) != "one" {
		t.Fatalf("first segment=%q", got)
	}
	if err := store.Seal(); err != nil {
		t.Fatalf("seal structural segments: %v", err)
	}
	if _, err := store.Append(t.Context(), []byte("late")); !errors.Is(err, errStructuralSegmentsSealed) {
		t.Fatalf("append after seal error=%v", err)
	}
	if _, err := store.Read(t.Context(), twoID+1); !errors.Is(err, errStructuralSegmentID) {
		t.Fatalf("read invalid id error=%v", err)
	}
}

func TestStructuralSegmentsSpillAndCleanup(t *testing.T) {
	dir := t.TempDir()
	before := structuralResidentBytes.Load()
	store, err := newStructuralSegments(dir, 24)
	if err != nil {
		t.Fatalf("create structural segments: %v", err)
	}
	firstID, err := store.Append(t.Context(), []byte("first"))
	if err != nil {
		t.Fatalf("append resident segment: %v", err)
	}
	secondID, err := store.Append(t.Context(), bytes.Repeat([]byte("s"), 32))
	if err != nil {
		t.Fatalf("append spilled segment: %v", err)
	}
	if store.spill == nil {
		t.Fatal("spill file was not opened")
	}
	spillName := store.spill.Name()
	if spillName == "" {
		t.Fatal("spill file name was empty")
	}
	for id, want := range map[uint64]string{firstID: "first", secondID: string(bytes.Repeat([]byte("s"), 32))} {
		got, readErr := store.Read(t.Context(), id)
		if readErr != nil {
			t.Fatalf("read spilled segment %d: %v", id, readErr)
		}
		if string(got) != want {
			t.Fatalf("spilled segment %d=%q", id, got)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close spilled segments: %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatalf("read spill directory after close: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("spill left directory entries after close: %v", entries)
	}
	if got := structuralResidentBytes.Load(); got != before {
		t.Fatalf("resident bytes after close=%d, want %d", got, before)
	}
}

func TestStructuralSegmentsCancellation(t *testing.T) {
	store, err := newStructuralSegments(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatalf("create structural segments: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Append(canceled, []byte("ignored")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled append error=%v", err)
	}
	if store.Bytes() != 0 {
		t.Fatalf("bytes after canceled append=%d", store.Bytes())
	}
}

func TestStructuralEncodedAccountingHasNoRepositoryBudget(t *testing.T) {
	before := structuralEncodedBytes.Load()
	if structuralSegmentsMaxBytes != int64(structuralOffsetMask) {
		t.Fatalf("per-segment bound = %d, want local address mask %d", structuralSegmentsMaxBytes, structuralOffsetMask)
	}
	if !reserveStructuralEncoded(17) {
		t.Fatal("small encoded-byte reservation failed")
	}
	structuralEncodedBytes.Add(-17)
	if after := structuralEncodedBytes.Load(); after != before {
		t.Fatalf("encoded bytes after release=%d, want %d", after, before)
	}
}

func TestStructuralSegmentsSpillIsNameless(t *testing.T) {
	dir := t.TempDir()
	store, err := newStructuralSegments(dir, 1)
	testutil.FailErr(t, "create structural segments", err)
	id, err := store.Append(t.Context(), []byte("spilled"))
	testutil.FailErr(t, "append spilled record", err)
	if store.spill == nil || store.spillID == 0 {
		t.Fatal("record past the memory limit did not spill")
	}
	// Unlinked spill files leave no scratch paths after process exit.
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "read spill directory", err)
	if len(entries) != 0 {
		t.Fatalf("spill directory entries = %d, want none", len(entries))
	}
	testutil.FailErr(t, "seal spilled segments", store.Seal())
	if store.spill == nil {
		t.Fatal("sealed segment dropped the only handle to its spill")
	}
	actual, err := store.Read(t.Context(), id)
	testutil.FailErr(t, "read sealed spilled record", err)
	if string(actual) != "spilled" {
		t.Fatalf("sealed spilled record=%q", actual)
	}
	testutil.FailErr(t, "close spilled segments", store.Close())
}

func TestStructuralSegmentsSealReturnsAppendBufferReservation(t *testing.T) {
	// Below one record the spill writes straight through and reserves nothing,
	// so the limit has to exceed a record for an append buffer to exist.
	store, err := newStructuralSegments(t.TempDir(), 128)
	testutil.FailErr(t, "create structural segments", err)
	defer func() { testutil.FailErr(t, "close structural segments", store.Close()) }()
	for range 2 {
		_, err = store.Append(t.Context(), bytes.Repeat([]byte("x"), 80))
		testutil.FailErr(t, "append spilled record", err)
	}
	if store.spill == nil || store.resident == 0 {
		t.Fatalf("fixture did not reserve a spill append buffer (spilled=%t, resident=%d)", store.spill != nil, store.resident)
	}
	before := structuralResidentBytes.Load()
	reserved := store.resident
	testutil.FailErr(t, "seal spilled segments", store.Seal())
	// Sealed segments release their append-buffer reservations.
	if store.resident != 0 {
		t.Fatalf("sealed segment retained %d resident bytes", store.resident)
	}
	if after := structuralResidentBytes.Load(); after != before-reserved {
		t.Fatalf("shared resident bytes = %d, want %d", after, before-reserved)
	}
}

func TestStructuralRetentionLeavesNoScratchToReclaim(t *testing.T) {
	dir := t.TempDir()
	store, err := newStructuralSegments(dir, 1)
	testutil.FailErr(t, "create structural segments", err)
	defer func() { testutil.FailErr(t, "close structural segments", store.Close()) }()
	id, err := store.Append(t.Context(), []byte("pinned spill"))
	testutil.FailErr(t, "append spilled record", err)
	testutil.FailErr(t, "seal spilled segments", store.Seal())

	catalog := New()
	catalog.treeDir = dir
	removed, err := catalog.ReconcileTreeStores(t.Context())
	testutil.FailErr(t, "reconcile structural caches", err)
	if removed != 0 {
		t.Fatalf("removed entries = %d, want none", removed)
	}
	body, err := store.Read(t.Context(), id)
	testutil.FailErr(t, "read live spill after retention", err)
	if string(body) != "pinned spill" {
		t.Fatalf("live spill body = %q", body)
	}
}

func TestStructuralSegmentsSpillReadCacheReusesCompleteBlocks(t *testing.T) {
	clearStructuralSpillBlockCacheForTest()
	store, err := newStructuralSegments(t.TempDir(), 1)
	testutil.FailErr(t, "create cached spill segments", err)
	defer func() { testutil.FailErr(t, "close cached spill segments", store.Close()) }()
	first, err := store.Append(t.Context(), bytes.Repeat([]byte("a"), structuralSegmentReadBlock-structuralRecordHeaderSize))
	testutil.FailErr(t, "append first cached record", err)
	second, err := store.Append(t.Context(), []byte("tail"))
	testutil.FailErr(t, "append second cached record", err)
	testutil.FailErr(t, "seal cached spill", store.Seal())
	if entries, _ := structuralSpillBlockCacheStatsForTest(); entries != 0 {
		t.Fatal("read cache populated before reads")
	}
	_, err = store.Read(t.Context(), first)
	testutil.FailErr(t, "read first cached record", err)
	entries, _ := structuralSpillBlockCacheStatsForTest()
	if entries != 1 {
		t.Fatalf("complete spill block cache entries = %d, want 1", entries)
	}
	_, err = store.Read(t.Context(), second)
	testutil.FailErr(t, "read second cached record", err)
	after, _ := structuralSpillBlockCacheStatsForTest()
	if after != entries {
		t.Fatalf("partial tail read populated additional cache entries: got %d, want %d", after, entries)
	}
}

func TestStructuralSegmentsSpillReadCacheDoesNotStalePartialTail(t *testing.T) {
	clearStructuralSpillBlockCacheForTest()
	store, err := newStructuralSegments(t.TempDir(), 1)
	testutil.FailErr(t, "create partial tail cached spill", err)
	defer func() { testutil.FailErr(t, "close partial tail cached spill", store.Close()) }()
	first, err := store.Append(t.Context(), []byte("first"))
	testutil.FailErr(t, "append first partial tail record", err)
	_, err = store.Read(t.Context(), first)
	testutil.FailErr(t, "read first partial tail record", err)
	if entries, _ := structuralSpillBlockCacheStatsForTest(); entries != 0 {
		t.Fatalf("partial tail cache entries after first read = %d, want 0", entries)
	}
	second, err := store.Append(t.Context(), []byte("second"))
	testutil.FailErr(t, "append second partial tail record", err)
	body, err := store.Read(t.Context(), second)
	testutil.FailErr(t, "read appended partial tail record before seal", err)
	if string(body) != "second" {
		t.Fatalf("partial tail body before seal = %q, want second", body)
	}
	testutil.FailErr(t, "seal partial tail cached spill", store.Seal())
	body, err = store.Read(t.Context(), second)
	testutil.FailErr(t, "read appended partial tail record after seal", err)
	if string(body) != "second" {
		t.Fatalf("partial tail body after seal = %q, want second", body)
	}
}

func TestStructuralSegmentsSpillReadCacheReadsAcrossBlockBoundary(t *testing.T) {
	clearStructuralSpillBlockCacheForTest()
	store, err := newStructuralSegments(t.TempDir(), 1)
	testutil.FailErr(t, "create boundary cached spill", err)
	defer func() { testutil.FailErr(t, "close boundary cached spill", store.Close()) }()
	_, err = store.Append(t.Context(), bytes.Repeat([]byte("a"), structuralSegmentReadBlock-structuralRecordHeaderSize-4))
	testutil.FailErr(t, "append boundary padding record", err)
	id, err := store.Append(t.Context(), []byte("tail"))
	testutil.FailErr(t, "append boundary target record", err)
	testutil.FailErr(t, "seal boundary cached spill", store.Seal())
	body, err := store.Read(t.Context(), id)
	testutil.FailErr(t, "read boundary cached record", err)
	if string(body) != "tail" {
		t.Fatalf("boundary body = %q, want tail", body)
	}
}

func TestStructuralSegmentsSpillReadCacheIsProcessBounded(t *testing.T) {
	clearStructuralSpillBlockCacheForTest()
	structuralSpillBlocks.mu.Lock()
	oldLimit := structuralSpillBlocks.limit
	structuralSpillBlocks.limit = int64(structuralSegmentReadBlock * 2)
	structuralSpillBlocks.mu.Unlock()
	t.Cleanup(func() {
		structuralSpillBlocks.mu.Lock()
		structuralSpillBlocks.limit = oldLimit
		structuralSpillBlocks.mu.Unlock()
		clearStructuralSpillBlockCacheForTest()
	})
	var stores []*structuralSegments
	t.Cleanup(func() {
		for _, store := range stores {
			testutil.FailErr(t, "close bounded cached spill", store.Close())
		}
	})
	for range 3 {
		store, err := newStructuralSegments(t.TempDir(), 32)
		testutil.FailErr(t, "create bounded cached spill", err)
		stores = append(stores, store)
		id, err := store.Append(t.Context(), bytes.Repeat([]byte("x"), structuralSegmentReadBlock-structuralRecordHeaderSize))
		testutil.FailErr(t, "append bounded cached record", err)
		testutil.FailErr(t, "seal bounded cached spill", store.Seal())
		_, err = store.Read(t.Context(), id)
		testutil.FailErr(t, "read bounded cached record", err)
	}
	entries, cachedBytes := structuralSpillBlockCacheStatsForTest()
	if entries != 2 {
		t.Fatalf("spill block cache entries=%d, want 2 after evicting the oldest live segment block", entries)
	}
	if cachedBytes > int64(structuralSegmentReadBlock*2) {
		t.Fatalf("spill block cache bytes=%d, want <=%d", cachedBytes, structuralSegmentReadBlock*2)
	}
}

func structuralSpillBlockCacheStatsForTest() (int, int64) {
	structuralSpillBlocks.mu.Lock()
	defer structuralSpillBlocks.mu.Unlock()
	return len(structuralSpillBlocks.entries), structuralSpillBlocks.bytes
}

func clearStructuralSpillBlockCacheForTest() {
	structuralSpillBlocks.mu.Lock()
	structuralSpillBlocks.entries = make(map[structuralSpillBlockKey]*structuralSpillBlockEntry)
	structuralSpillBlocks.lru.Init()
	structuralSpillBlocks.bytes = 0
	structuralSpillBlocks.mu.Unlock()
}

type cancelOnHeaderContext struct {
	context.Context
	checks int
}

func (c *cancelOnHeaderContext) Err() error {
	c.checks++
	if c.checks > 1 {
		return context.Canceled
	}
	return nil
}

func TestStructuralReadPreservesCancellationDuringSpillRead(t *testing.T) {
	store, err := newStructuralSegments(t.TempDir(), 0)
	testutil.FailErr(t, "create spilled structure", err)
	defer func() { _ = store.Close() }()
	id, err := store.Append(t.Context(), bytes.Repeat([]byte("x"), structuralSegmentReadBlock*2))
	testutil.FailErr(t, "append flushed record", err)
	ctx := &cancelOnHeaderContext{Context: t.Context()}
	if _, err := store.Read(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted read reported corrupt identity: %v", err)
	}
}
