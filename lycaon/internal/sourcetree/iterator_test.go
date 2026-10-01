package sourcetree

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFrameIteratorMatchesRankSelectionAcrossNestedRanges(t *testing.T) {
	view, root := viewFixture(t)
	for i := range 12 {
		for _, name := range []string{"top.txt", "a/deep/first.txt", "a/deep/second.txt", "b/last.txt"} {
			file := filepath.Join(root.Path, fmt.Sprintf("dir-%02d", i), name)
			testutil.FailErr(t, "create nested fixture", os.MkdirAll(filepath.Dir(file), 0700))
			testutil.FailErr(t, "write fixture", os.WriteFile(file, []byte("source"), 0600))
		}
	}
	observeFixture(t, view, root)
	testutil.FailErr(t, "collapse sparse branch", view.Disclose(t.Context(), nil, IntentEntry{Address: Address{Root: root.ID, Path: "dir-04/a"}, Disclosure: Disclosure{Open: false, Recursive: false}}))
	snapshot, err := view.snapshot(t.Context())
	testutil.FailErr(t, "open stable projection", err)
	defer snapshot.Close()
	projection := &snapshot.roots[0].projection
	total, err := projection.Count(t.Context())
	testutil.FailErr(t, "read extent", err)
	for _, offset := range []int64{0, 1, 3, 11, 27, total - 9, total - 1, total} {
		for _, limit := range []int{1, 7, 31, 200} {
			rows, extent, err := projection.Frame(t.Context(), offset, limit)
			testutil.FailErr(t, "read sequential frame", err)
			if extent != total || len(rows) != min(limit, int(total-offset)) {
				t.Fatalf("span %d/%d extent=%d rows=%d", offset, limit, extent, len(rows))
			}
			for i, row := range rows {
				expected, err := projection.Select(t.Context(), offset+int64(i))
				testutil.FailErr(t, "select rank oracle", err)
				if !reflect.DeepEqual(row, expected) {
					t.Fatalf("row %d got=%+v want=%+v", offset+int64(i), row, expected)
				}
			}
		}
	}
}
