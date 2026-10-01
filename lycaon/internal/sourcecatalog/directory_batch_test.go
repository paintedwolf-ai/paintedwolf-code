package sourcecatalog

import (
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type directoryBatchFixture struct {
	os.DirEntry
	name string
}

func (e directoryBatchFixture) Name() string { return e.name }

type directoryBatchReader struct {
	remaining int
	short     bool
	finalEOF  bool
}

func (r *directoryBatchReader) ReadDir(n int) ([]directoryEntry, error) {
	if r.short {
		n = min(n, 3)
	}
	n = min(n, r.remaining)
	entries := make([]directoryEntry, 0, n)
	for range n {
		entries = append(entries, directoryBatchFixture{name: fmt.Sprint(r.remaining)})
		r.remaining--
	}
	if n == 0 || r.finalEOF && r.remaining == 0 {
		return entries, io.EOF
	}
	return entries, nil
}

func TestDirectoryBatchPreservesBoundedMembershipAtEndOfDirectory(t *testing.T) {
	for _, count := range []int{0, 1, 255, 256, 257, 512, 513} {
		for _, short := range []bool{false, true} {
			for _, finalEOF := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/short=%v/finalEOF=%v", count, short, finalEOF), func(t *testing.T) {
					reader := directoryBatch{file: &directoryBatchReader{remaining: count, short: short, finalEOF: finalEOF}}
					seen := make(map[string]bool)
					for {
						entries, complete, err := reader.read(indexBatchSize)
						testutil.FailErr(t, "read bounded directory batch", err)
						if len(entries) > indexBatchSize {
							t.Fatalf("batch has %d entries", len(entries))
						}
						for _, entry := range entries {
							if seen[entry.Name()] {
								t.Fatalf("duplicate entry %s", entry.Name())
							}
							seen[entry.Name()] = true
						}
						if complete {
							break
						}
						if len(entries) != indexBatchSize {
							t.Fatal("short batch did not report completion")
						}
					}
					if len(seen) != count {
						t.Fatalf("read %d of %d entries", len(seen), count)
					}
				})
			}
		}
	}
}
