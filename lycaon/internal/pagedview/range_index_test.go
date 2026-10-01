package pagedview

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type countedPages struct {
	MemoryPages[int]
	reads int
}

func (s *countedPages) Read(ctx context.Context, id uint64) (RangePage[int], error) {
	s.reads++
	return s.MemoryPages.Read(ctx, id)
}

func TestRangeIndexDistantSelectionAndWeightRepair(t *testing.T) {
	ctx := context.Background()
	store := &countedPages{}
	index := RangeIndex[int]{Store: store}
	const count = 20000
	for i := count - 1; i >= 0; i-- {
		testutil.FailErr(t, "insert range", index.Set(ctx, RangeItem[int]{Key: fmt.Sprintf("%08d", i), Value: i, Weight: 500}))
	}
	total, err := index.Extent(ctx)
	testutil.FailErr(t, "extent", err)
	if total != 10000000 {
		t.Fatalf("extent: %d", total)
	}
	store.reads = 0
	item, offset, err := index.Select(ctx, total-1)
	testutil.FailErr(t, "distant selection", err)
	if item.Value != count-1 || offset != 499 || store.reads > 4 {
		t.Fatalf("distant range: %+v offset %d reads %d", item, offset, store.reads)
	}
	testutil.FailErr(t, "collapse range", index.Set(ctx, RangeItem[int]{Key: "00000002", Value: 2, Weight: 1}))
	_, rank, err := index.Locate(ctx, "00019999")
	testutil.FailErr(t, "locate after repair", err)
	if rank != int64((count-1)*500-499) {
		t.Fatalf("repaired rank: %d", rank)
	}
	for i := 0; i < count; i++ {
		testutil.FailErr(t, "remove range", index.Remove(ctx, fmt.Sprintf("%08d", i)))
	}
	if index.Root != 0 || len(store.pages) != 0 {
		t.Fatalf("retained empty index: root %d pages %d", index.Root, len(store.pages))
	}
}

func TestRangeBatchMatchesIndividualUpdates(t *testing.T) {
	ctx := context.Background()
	store := &MemoryPages[int]{}
	index := RangeIndex[int]{Store: store}
	for round := 0; round < 10; round++ {
		batch := make([]RangeItem[int], 0, 256)
		for i := 255; i >= 0; i-- {
			value := i*10 + round
			batch = append(batch, RangeItem[int]{Key: fmt.Sprintf("%08d", value), Value: value, Weight: 1})
		}
		testutil.FailErr(t, "publish batch", index.SetBatch(ctx, batch))
	}
	for i := 0; i < 2560; i++ {
		item, _, err := index.Select(ctx, int64(i))
		testutil.FailErr(t, "select batch item", err)
		if item.Value != i {
			t.Fatalf("rank %d returned %d", i, item.Value)
		}
	}
}

func TestRangePrefixFingerprintUsesOnlyTheSearchPath(t *testing.T) {
	store := &countedPages{}
	index := RangeIndex[int]{Store: store}
	var expected Fingerprint
	var baseline Fingerprint
	for i := 0; i < 20000; i++ {
		item := RangeItem[int]{Key: fmt.Sprintf("%08d", i), Value: i, Weight: 3, Fingerprint: FingerprintOf([]byte(fmt.Sprintf("row-%d", i))), BaselineFingerprint: FingerprintOf([]byte(fmt.Sprintf("base-%d", i)))}
		testutil.FailErr(t, "insert fingerprint", index.Set(t.Context(), item))
		if i < 17003 {
			expected = expected.Combine(item.Fingerprint)
			baseline = baseline.Combine(item.BaselineFingerprint)
		}
	}
	for _, closed := range []bool{false, true} {
		store.reads = 0
		actual, err := index.PrefixFingerprint(t.Context(), "00017003", closed)
		testutil.FailErr(t, "read prefix fingerprint", err)
		want := expected
		if closed {
			want = baseline
		}
		if actual != want || store.reads > 4 {
			t.Fatalf("prefix=%x want=%x pages=%d", actual, want, store.reads)
		}
	}
}

func TestRangeCoverageSurvivesSplitsAndRemoval(t *testing.T) {
	index := RangeIndex[int]{Store: &MemoryPages[int]{}}
	items := make([]RangeItem[int], 1000)
	for i := range items {
		items[i] = RangeItem[int]{Key: fmt.Sprintf("%04d", i), Value: i, Weight: 1, Unresolved: 1}
	}
	testutil.FailErr(t, "publish incomplete ranges", index.SetBatch(t.Context(), items))
	pending, err := index.Unresolved(t.Context())
	testutil.FailErr(t, "read incomplete contributions", err)
	if pending != 1000 {
		t.Fatalf("unresolved=%d", pending)
	}
	for i := range items[:500] {
		items[i].Unresolved = 0
	}
	testutil.FailErr(t, "settle first half", index.SetBatch(t.Context(), items[:500]))
	testutil.FailErr(t, "remove pending contribution", index.Remove(t.Context(), "0999"))
	pending, err = index.Unresolved(t.Context())
	testutil.FailErr(t, "read remaining contributions", err)
	if pending != 499 {
		t.Fatalf("unresolved=%d", pending)
	}
}
