package sourcecatalog

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDecodedPageCacheChargesExpandedPaths(t *testing.T) {
	prefix := strings.Repeat("directory/", 400)
	page := pagedview.RangePage[TreeItem]{}
	for i := range pagedview.PageFanout {
		name := fmt.Sprintf("file-%03d", i)
		page.Items = append(page.Items, pagedview.RangeItem[TreeItem]{Key: name, Value: TreeItem{Path: prefix + name}, Weight: 1})
	}
	body, err := encodeRangePage(page)
	testutil.FailErr(t, "encode compressed page", err)
	cache := structurePageCache{}
	for i := range uint64(64) {
		decoded, err := decodeRangePage(body)
		testutil.FailErr(t, "decode retained page", err)
		cache.put(structurePageKey{store: 1, id: i}, decoded)
	}
	if _, held := cache.get(structurePageKey{store: 1, id: 0}); held {
		t.Fatal("compressed byte counts retained more than the decoded memory budget")
	}
	if _, held := cache.get(structurePageKey{store: 1, id: 63}); !held {
		t.Fatal("latest decoded page was not retained")
	}
}
