package tooloutput

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isolateConfigHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
}

func TestFetchCacheHashStableAndAddressed(t *testing.T) {
	a := fetchCacheHash("https://example.com/x")
	b := fetchCacheHash("https://example.com/x")
	c := fetchCacheHash("https://example.com/y")
	if a != b {
		t.Fatalf("same URL must map to same hash: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("different URLs must differ: %q", a)
	}
}

func TestWriteAndReadFetchCacheRoundTrip(t *testing.T) {
	isolateConfigHome(t)
	url := "https://example.com/roundtrip"
	body := "# Heading\n\nbody\n"
	entry := FetchCacheEntry{
		URL: "https://example.com/final", Status: 203, ContentType: "text/markdown",
		Title: "Heading", Body: body, Ext: "md", Markdown: true,
	}

	if err := WriteFetchCache(url, entry); err != nil {
		t.Fatalf("WriteFetchCache: %v", err)
	}
	got, ok := ReadFetchCache(url)
	if !ok || got != entry {
		t.Fatalf("ReadFetchCache = %#v,%v want %#v", got, ok, entry)
	}
}

func TestWriteFetchCacheOverwritesToRefresh(t *testing.T) {
	isolateConfigHome(t)
	url := "https://example.com/refresh"

	if err := WriteFetchCache(url, FetchCacheEntry{URL: url, Status: 200, Body: "first", Ext: "txt"}); err != nil {
		t.Fatalf("write1: %v", err)
	}
	// A real re-fetch (after a TTL miss) must refresh content, not keep the stale body.
	if err := WriteFetchCache(url, FetchCacheEntry{URL: url, Status: 200, Body: "second", Ext: "txt"}); err != nil {
		t.Fatalf("write2: %v", err)
	}
	got, ok := ReadFetchCache(url)
	if !ok || got.Body != "second" {
		t.Fatalf("cache should refresh on write: got %#v ok=%v", got, ok)
	}
}

func TestWriteFetchCacheReplacesPriorExtension(t *testing.T) {
	isolateConfigHome(t)
	url := "https://example.com/changes-ext"
	if err := WriteFetchCache(url, FetchCacheEntry{URL: url, Status: 200, Body: "as text", Ext: "txt"}); err != nil {
		t.Fatalf("write txt: %v", err)
	}
	if err := WriteFetchCache(url, FetchCacheEntry{URL: url, Status: 200, Body: "as markdown", Ext: "md"}); err != nil {
		t.Fatalf("write md: %v", err)
	}
	dir, _ := fetchCacheDir()
	matches, _ := filepath.Glob(filepath.Join(dir, fetchCacheHash(url)+".*"))
	if len(matches) != 1 {
		t.Fatalf("a URL must keep exactly one cache entry, got %d: %v", len(matches), matches)
	}
	got, ok := ReadFetchCache(url)
	if !ok || got.Ext != "md" {
		t.Fatalf("expected refreshed ext=md, got %#v ok=%v", got, ok)
	}
}

func TestReadFetchCacheMiss(t *testing.T) {
	isolateConfigHome(t)
	if _, ok := ReadFetchCache("https://example.com/absent"); ok {
		t.Fatal("expected miss for absent entry")
	}
}

func TestReadFetchCacheTTLExpiry(t *testing.T) {
	isolateConfigHome(t)
	url := "https://example.com/stale"
	if err := WriteFetchCache(url, FetchCacheEntry{URL: url, Status: 200, Body: "old body", Ext: "txt"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	dir, _ := fetchCacheDir()
	matches, _ := filepath.Glob(filepath.Join(dir, fetchCacheHash(url)+".*"))
	if len(matches) != 1 {
		t.Fatalf("expected one entry, got %v", matches)
	}
	old := time.Now().Add(-fetchCacheTTL - time.Hour)
	if err := os.Chtimes(matches[0], old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if _, ok := ReadFetchCache(url); ok {
		t.Fatal("stale entry must be a miss")
	}
	if _, statErr := os.Stat(matches[0]); statErr == nil {
		t.Fatal("stale entry should be removed on read")
	}
}

func TestEvictFetchCacheBoundsSize(t *testing.T) {
	isolateConfigHome(t)
	dir, _ := fetchCacheDir()
	big := strings.Repeat("x", 1<<20)
	for i := 0; i < (fetchCacheMaxBytes/len(big))+8; i++ {
		url := "https://example.com/page" + strings.Repeat("z", i+1)
		if err := WriteFetchCache(url, FetchCacheEntry{URL: url, Status: 200, Body: big, Ext: "txt"}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	var total int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	if total > fetchCacheMaxBytes {
		t.Fatalf("cache size %d exceeds cap %d after eviction", total, fetchCacheMaxBytes)
	}
}
