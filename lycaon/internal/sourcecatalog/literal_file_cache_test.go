package sourcecatalog

import "testing"

func TestLiteralFileCacheDropsOnlyTheChangedSubtree(t *testing.T) {
	var cache literalFileCache
	for _, rel := range []string{"a/b.go", "a/c/d.go", "ab.go", "a.go"} {
		cache.store("/root", rel, literalFileCacheEntry{size: 1})
	}
	cache.store("/other", "a/b.go", literalFileCacheEntry{size: 1})
	total := cache.bytes

	cache.drop("/root", "a")
	for rel, want := range map[string]bool{"a/b.go": false, "a/c/d.go": false, "ab.go": true, "a.go": true} {
		if _, ok := cache.lookup("/root", rel); ok != want {
			t.Errorf("after dropping a: %s cached=%v want %v", rel, ok, want)
		}
	}
	if _, ok := cache.lookup("/other", "a/b.go"); !ok {
		t.Fatal("drop crossed roots")
	}
	if cache.count != 3 || cache.bytes >= total {
		t.Fatalf("count=%d bytes=%d (was %d)", cache.count, cache.bytes, total)
	}

	cache.drop("/root", "missing/file.go")
	cache.drop("/root", "ab.go")
	if _, ok := cache.lookup("/root", "ab.go"); ok || cache.count != 2 {
		t.Fatalf("exact drop: count=%d", cache.count)
	}
	cache.drop("/root", ".")
	if _, ok := cache.roots["/root"]; ok || cache.count != 1 {
		t.Fatalf("root drop left roots=%v count=%d", cache.roots, cache.count)
	}
	cache.drop("/other", "a/b.go")
	if len(cache.roots) != 0 || cache.count != 0 || cache.bytes != 0 {
		t.Fatalf("emptied cache kept roots=%v count=%d bytes=%d", cache.roots, cache.count, cache.bytes)
	}
}

func TestLiteralFileCacheEvictionSkipsReplacedObservations(t *testing.T) {
	var cache literalFileCache
	cache.store("/root", "a.go", literalFileCacheEntry{size: 1})
	first := cache.order[0]
	cache.store("/root", "a.go", literalFileCacheEntry{size: 2})
	cache.evict(first)
	if entry, ok := cache.lookup("/root", "a.go"); !ok || entry.size != 2 {
		t.Fatalf("stale ref evicted the replacement: %+v %v", entry, ok)
	}
	cache.compact()
	if len(cache.order) != 1 {
		t.Fatalf("compact kept %d refs, want 1", len(cache.order))
	}
	cache.evict(cache.order[0])
	if _, ok := cache.lookup("/root", "a.go"); ok || cache.count != 0 || cache.bytes != 0 || len(cache.roots) != 0 {
		t.Fatalf("evicted entry remains: count=%d bytes=%d roots=%v", cache.count, cache.bytes, cache.roots)
	}
}
