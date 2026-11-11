package scan

import (
	"slices"
	"testing"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
)

func TestSeriesFaultFactsBelongToOneStoreLifetime(t *testing.T) {
	left, right := NewSQLStore(nil), NewSQLStore(nil)
	for _, path := range []string{"/z", "/a"} {
		storeSeriesRuntimeState(&left.sqlCore, SeriesRow{
			CanonicalPath: path, ScannerID: "scanner",
			UnavailableReason: scancatalog.ConfigFaultExecutableUnavailable,
		})
	}
	if got := UnavailableSeriesPaths(left); !slices.Equal(got, []string{"/a", "/z"}) {
		t.Fatalf("unavailable paths = %v", got)
	}
	if got := UnavailableSeriesPaths(right); len(got) != 0 {
		t.Fatalf("another store retained fault facts: %v", got)
	}
	for _, path := range []string{"/z", "/a"} {
		if err := finishDeleteSeries(&left.sqlCore, path, "scanner", nil); err != nil {
			t.Fatalf("delete series state: %v", err)
		}
	}
	if got := UnavailableSeriesPaths(left); len(got) != 0 {
		t.Fatalf("deleted series retained fault facts: %v", got)
	}
}
