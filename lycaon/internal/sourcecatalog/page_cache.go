package sourcecatalog

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/lycaon/lycaon/internal/pagedview"
)

var pageCacheSerial atomic.Uint64

// Compressed page bytes do not bound the decoded paths retained in memory.
func rangePageBytes(page pagedview.RangePage[TreeItem]) int64 {
	bytes := int64(unsafe.Sizeof(page)) + 128
	bytes += int64(cap(page.Items)) * int64(unsafe.Sizeof(pagedview.RangeItem[TreeItem]{}))
	bytes += int64(cap(page.Children)) * int64(unsafe.Sizeof(pagedview.Branch{}))
	for _, item := range page.Items {
		bytes += int64(len(item.Key) + len(item.Value.Path))
	}
	for _, child := range page.Children {
		bytes += int64(len(child.Key))
	}
	return bytes
}

type structurePageKey struct{ store, id uint64 }

// Only committed immutable pages enter the shared cache.
type structurePageCache struct {
	mu    sync.Mutex
	pages *pagedview.Cache[structurePageKey, pagedview.RangePage[TreeItem]]
}

func (c *structurePageCache) get(key structurePageKey) (pagedview.RangePage[TreeItem], bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pages == nil {
		return pagedview.RangePage[TreeItem]{}, false
	}
	return c.pages.Get(key)
}

func (c *structurePageCache) put(key structurePageKey, page pagedview.RangePage[TreeItem]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pages == nil {
		c.pages = pagedview.NewCache[structurePageKey, pagedview.RangePage[TreeItem]](2048, 16<<20)
	}
	c.pages.Put(key, page, rangePageBytes(page))
}
