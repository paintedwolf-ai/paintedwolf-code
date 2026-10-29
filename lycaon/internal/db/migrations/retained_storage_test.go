package migrations

import (
	"math"
	"testing"
)

func TestSorterStorageAccountsRecordsRunsAndOversizedWindows(t *testing.T) {
	small, err := sorterStorageBytes(8<<20, 100000, 128, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if small < 8<<20+18*100000+64<<20 {
		t.Fatalf("sorter bound omits framing: %d", small)
	}
	huge, err := sorterStorageBytes(8<<20, 100000, 4<<20, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if huge <= small {
		t.Fatalf("large record did not increase merge windows: %d <= %d", huge, small)
	}
	more, err := sorterStorageBytes(16<<20, 200000, 128, 4096)
	if err != nil || more <= small {
		t.Fatalf("larger input bound=%d: %v", more, err)
	}
	for _, v := range [][4]uint64{{math.MaxUint64, 1, 1, 4096}, {1, math.MaxUint64, 1, 4096}, {1, 1, math.MaxUint64, 4096}, {1, 1, 1, 1000}} {
		if _, err := sorterStorageBytes(v[0], v[1], v[2], v[3]); err == nil {
			t.Fatalf("accepted invalid/overflow %v", v)
		}
	}
}
