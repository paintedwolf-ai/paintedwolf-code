package pagedview

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWeightedUnionSeeksSparseAdditionsAndOverlappingKeys(t *testing.T) {
	ctx := t.Context()
	base := &RangeIndex[string]{Store: &MemoryPages[string]{}}
	added := &RangeIndex[string]{Store: &MemoryPages[string]{}}
	weights := map[string]int64{}
	for i := 0; i < 900; i++ {
		key := fmt.Sprintf("%06d", i*3)
		item := RangeItem[string]{Key: key, Value: key, Weight: int64(i % 7)}
		testutil.FailErr(t, "add base range", base.Set(ctx, item))
		weights[key] += item.Weight
	}
	for i := 0; i < 400; i++ {
		key := fmt.Sprintf("%06d", i*5)
		item := RangeItem[string]{Key: key, Value: key, Weight: int64(i % 5)}
		testutil.FailErr(t, "add sparse range", added.Set(ctx, item))
		weights[key] += item.Weight
	}
	union := WeightedUnion[string]{Base: base, Added: added}
	keys := make([]string, 0, len(weights))
	for key := range weights {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rank := int64(0)
	for _, key := range keys {
		item, prefix, err := union.Locate(ctx, key)
		testutil.FailErr(t, "locate union key", err)
		if prefix != rank || item.Weight != weights[key] {
			t.Fatalf("key=%s prefix=%d weight=%d want %d %d", key, prefix, item.Weight, rank, weights[key])
		}
		for offset := int64(0); offset < weights[key]; offset++ {
			selected, at, err := union.Select(ctx, rank+offset)
			testutil.FailErr(t, "select union row", err)
			if selected.Key != key || at != offset {
				t.Fatalf("rank=%d key=%s offset=%d want %s %d", rank+offset, selected.Key, at, key, offset)
			}
		}
		rank += weights[key]
	}
	total, err := union.Extent(ctx)
	testutil.FailErr(t, "read union extent", err)
	if total != rank {
		t.Fatalf("extent=%d want %d", total, rank)
	}
}

type countedWeights struct {
	OrderedWeights[string]
	probes int
}

func (c *countedWeights) Locate(ctx context.Context, key string) (RangeItem[string], int64, error) {
	c.probes++
	return c.OrderedWeights.Locate(ctx, key)
}

func TestWeightedUnionDistantLookupHasBoundedKeyProbes(t *testing.T) {
	base := &RangeIndex[string]{Store: &MemoryPages[string]{}}
	added := &RangeIndex[string]{Store: &MemoryPages[string]{}}
	batch := make([]RangeItem[string], 10000)
	for i := range batch {
		key := fmt.Sprintf("%08d", i*2)
		batch[i] = RangeItem[string]{Key: key, Value: key, Weight: 1000}
	}
	testutil.FailErr(t, "build large logical tree", base.SetBatch(t.Context(), batch))
	testutil.FailErr(t, "add distant deleted row", added.Set(t.Context(), RangeItem[string]{Key: "00019997", Value: "deleted", Weight: 1}))
	counted := &countedWeights{OrderedWeights: base}
	union := WeightedUnion[string]{Base: counted, Added: added}
	row, offset, err := union.Select(t.Context(), 9_999_000)
	testutil.FailErr(t, "seek ten-million-row overlay", err)
	if row.Value != "deleted" || offset != 0 || counted.probes > 20 {
		t.Fatalf("row=%+v offset=%d probes=%d", row, offset, counted.probes)
	}
}
