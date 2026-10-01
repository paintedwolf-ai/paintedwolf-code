package paginate_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/paginate"
)

func TestSlice(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	page, total, truncated, next := paginate.Slice(items, 1, 2)
	if total != 5 || len(page) != 2 || page[0] != 2 || page[1] != 3 {
		t.Fatalf("page = %v total=%d", page, total)
	}
	if !truncated || next == nil || *next != 3 {
		t.Fatalf("truncated=%v next=%v", truncated, next)
	}
}
