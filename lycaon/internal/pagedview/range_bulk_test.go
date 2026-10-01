package pagedview

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRangeBulkBuildStreamsPackedLevels(t *testing.T) {
	for _, count := range []int{0, 1, PageFanout, PageFanout + 1, PageFanout*PageFanout + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			pages := &MemoryPages[int]{}
			index := RangeIndex[int]{Store: pages}
			err := index.BuildSorted(t.Context(), func(yield func(RangeItem[int]) error) error {
				for i := 0; i < count; i++ {
					if err := yield(RangeItem[int]{Key: fmt.Sprintf("%08d", i), Value: i, Weight: 3}); err != nil {
						return err
					}
				}
				return nil
			})
			testutil.FailErr(t, "stream packed range", err)
			got, err := index.Count(t.Context())
			testutil.FailErr(t, "count packed range", err)
			if got != int64(count) {
				t.Fatalf("count=%d", got)
			}
			if count == 0 {
				if index.Root != 0 {
					t.Fatal("empty stream allocated root")
				}
				return
			}
			item, offset, err := index.Select(t.Context(), int64(count*3-1))
			testutil.FailErr(t, "select last packed item", err)
			if item.Value != count-1 || offset != 2 {
				t.Fatalf("last=%+v offset=%d", item, offset)
			}
			maximum := (count+PageFanout-1)/PageFanout + 4
			if len(pages.pages) > maximum {
				t.Fatalf("packed build retained %d pages, maximum %d", len(pages.pages), maximum)
			}
		})
	}
}

func TestRangeBulkBuildRejectsUnorderedAndCanceledStreams(t *testing.T) {
	index := RangeIndex[int]{Store: &MemoryPages[int]{}}
	err := index.BuildSorted(t.Context(), func(yield func(RangeItem[int]) error) error {
		if err := yield(RangeItem[int]{Key: "b", Weight: 1}); err != nil {
			return err
		}
		return yield(RangeItem[int]{Key: "a", Weight: 1})
	})
	if !errors.Is(err, ErrWeight) {
		t.Fatalf("unsorted stream=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = index.BuildSorted(ctx, func(yield func(RangeItem[int]) error) error { return yield(RangeItem[int]{Key: "a", Weight: 1}) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stream=%v", err)
	}
}
