package pagedview

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRangeReadAfterSeeksAcrossPagesAndMissingKeys(t *testing.T) {
	store := &countedPages{}
	index := RangeIndex[int]{Store: store}
	var items []RangeItem[int]
	for i := range 20_000 {
		items = append(items, RangeItem[int]{Key: fmt.Sprintf("%08d", i*2), Value: i, Weight: int64(i % 7)})
	}
	testutil.FailErr(t, "build ordered ranges", index.SetBatch(t.Context(), items))
	for _, test := range []struct {
		after        string
		first, count int
	}{
		{"", 0, 200},
		{"00033999", 17_000, 200},
		{"00034000", 17_001, 200},
		{"00039994", 19_998, 2},
		{"99999999", 20_000, 0},
	} {
		store.reads = 0
		page, err := index.ReadAfter(t.Context(), test.after, 200)
		testutil.FailErr(t, "read bounded page", err)
		if len(page) != test.count || store.reads > 7 {
			t.Fatalf("after %q: count=%d reads=%d", test.after, len(page), store.reads)
		}
		for i, item := range page {
			if item.Value != test.first+i {
				t.Fatalf("after %q: item %d=%d", test.after, i, item.Value)
			}
		}
	}
}

func TestRangeReadAfterContinuesWhenCursorWasRemoved(t *testing.T) {
	index := RangeIndex[int]{Store: &MemoryPages[int]{}}
	testutil.FailErr(t, "seed range", index.SetBatch(t.Context(), []RangeItem[int]{
		{Key: "a", Value: 1}, {Key: "b", Value: 2}, {Key: "c", Value: 3},
	}))
	testutil.FailErr(t, "remove cursor", index.Remove(t.Context(), "b"))
	page, err := index.ReadAfter(t.Context(), "b", 1)
	testutil.FailErr(t, "continue after removed cursor", err)
	if len(page) != 1 || page[0].Key != "c" {
		t.Fatalf("continuation=%+v", page)
	}
}
