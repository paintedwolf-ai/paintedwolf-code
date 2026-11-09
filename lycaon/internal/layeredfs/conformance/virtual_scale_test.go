package conformance

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// TestVirtualScaleFixtureDistinctCardinalities proves the virtual fixture represents
// distinct logical scales: ReadDir("src") returns exactly logicalEntryCount entries
// for both the 1K and 2M cardinalities used by the mounted-claim scale contract (#1667).
// This fixture qualifies root-level operation counts only; it is not a real 2M-entry
// filesystem workload (actual filesystem workloads are identified separately).
func TestVirtualScaleFixtureDistinctCardinalities(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int64
	}{
		{"1k entries", 1_000},
		{"2m entries", 2_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newVirtualScaleLowerProvider(tc.n)
			entries, err := v.ReadDir("src")
			if err != nil {
				t.Fatalf("ReadDir(src): %v", err)
			}
			if int64(len(entries)) != tc.n {
				t.Fatalf("ReadDir(src) cardinality = %d; want %d", len(entries), tc.n)
			}
			if entries[0].Name != "file-0" {
				t.Fatalf("first entry = %q; want file-0", entries[0].Name)
			}
			last := fmt.Sprintf("file-%d", tc.n-1)
			if entries[len(entries)-1].Name != last {
				t.Fatalf("last entry = %q; want %q", entries[len(entries)-1].Name, last)
			}
			for _, e := range entries {
				if e.Kind != layeredfs.EntryRegular {
					t.Fatalf("entry %q kind = %v; want regular", e.Name, e.Kind)
				}
			}
		})
	}
}

// TestVirtualScaleFixtureOutOfRangeLookups proves the fixture is bounded: names beyond
// logicalEntryCount and non-numeric names return os.ErrNotExist, while the last in-range
// name resolves (#1667).
func TestVirtualScaleFixtureOutOfRangeLookups(t *testing.T) {
	t.Run("1k rejects index 1000", func(t *testing.T) {
		v := newVirtualScaleLowerProvider(1_000)
		if _, err := v.Lstat("src/file-1000"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Lstat(src/file-1000) on 1k fixture: err = %v; want ErrNotExist", err)
		}
	})
	t.Run("2m accepts last index and rejects beyond", func(t *testing.T) {
		v := newVirtualScaleLowerProvider(2_000_000)
		if _, err := v.Lstat("src/file-1999999"); err != nil {
			t.Fatalf("Lstat(src/file-1999999) on 2m fixture: %v", err)
		}
		if _, err := v.Lstat("src/file-2000000"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Lstat(src/file-2000000) on 2m fixture: err = %v; want ErrNotExist", err)
		}
	})
	t.Run("non-numeric and negative names rejected", func(t *testing.T) {
		v := newVirtualScaleLowerProvider(1_000)
		for _, name := range []string{"src/file-abc", "src/file-", "src/file--1", "src/other"} {
			if _, err := v.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Lstat(%q): err = %v; want ErrNotExist", name, err)
			}
		}
	})
	t.Run("OpenRead follows the same bound", func(t *testing.T) {
		v := newVirtualScaleLowerProvider(1_000)
		if _, _, err := v.OpenRead("src/file-1000"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("OpenRead(src/file-1000): err = %v; want ErrNotExist", err)
		}
		rc, entry, err := v.OpenRead("src/file-0")
		if err != nil {
			t.Fatalf("OpenRead(src/file-0): %v", err)
		}
		defer rc.Close()
		if entry.Size != 16 {
			t.Fatalf("OpenRead(src/file-0) size = %d; want 16", entry.Size)
		}
	})
	t.Run("fixture instruments its own enumeration", func(t *testing.T) {
		v := newVirtualScaleLowerProvider(10)
		if _, err := v.ReadDir("src"); err != nil {
			t.Fatalf("ReadDir(src): %v", err)
		}
		if _, err := v.Lstat("src/file-3"); err != nil {
			t.Fatalf("Lstat(src/file-3): %v", err)
		}
		if got := v.dirReads.Load(); got != 1 {
			t.Fatalf("dirReads = %d; want 1", got)
		}
		if got := v.lookups.Load(); got != 1 {
			t.Fatalf("lookups = %d; want 1", got)
		}
	})
}