package tooloutput

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/blobcache"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// FetchCacheEntry preserves the response metadata needed to render a cached
// fetch exactly as the live response was rendered.
type FetchCacheEntry struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type,omitempty"`
	Title       string `json:"title,omitempty"`
	Body        string `json:"body"`
	Ext         string `json:"ext"`
	Markdown    bool   `json:"markdown,omitempty"`
}

const (
	// fetchCacheTTL expires unused responses.
	fetchCacheTTL = 24 * time.Hour
	// fetchCacheMaxBytes caps total cache size; the oldest entries are evicted on
	// write once the directory exceeds it.
	fetchCacheMaxBytes = 64 << 20
)

// fetchCacheDir stores host-paged response bodies outside project trees.
func fetchCacheDir() (string, error) {
	base, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := enginepaths.FetchCacheRootUnder(base)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func fetchCacheHash(rawURL string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(rawURL)))
	return hex.EncodeToString(sum[:])[:16]
}

func sanitizeExt(ext string) string {
	// Fetch cache uses "txt" for unknown extensions.
	out := blobcache.SanitizeExt(ext)
	if out == "bin" {
		return "txt"
	}
	return out
}

// ReadFetchCache removes stale or corrupt entries and touches hits for LRU eviction.
func ReadFetchCache(rawURL string) (FetchCacheEntry, bool) {
	dir, err := fetchCacheDir()
	if err != nil {
		return FetchCacheEntry{}, false
	}
	hash := fetchCacheHash(rawURL)
	path := filepath.Join(dir, hash+".json")
	info, err := os.Stat(path)
	if err != nil {
		return FetchCacheEntry{}, false
	}
	if time.Since(info.ModTime()) > fetchCacheTTL {
		_ = os.Remove(path)
		return FetchCacheEntry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return FetchCacheEntry{}, false
	}
	var entry FetchCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil || strings.TrimSpace(entry.URL) == "" {
		_ = os.Remove(path)
		return FetchCacheEntry{}, false
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	entry.Ext = sanitizeExt(entry.Ext)
	return entry, true
}

// WriteFetchCache refreshes a URL response and evicts entries beyond the size cap.
func WriteFetchCache(rawURL string, entry FetchCacheEntry) error {
	dir, err := fetchCacheDir()
	if err != nil {
		return err
	}
	hash := fetchCacheHash(rawURL)
	blobcache.RemoveByPrefix(dir, hash)
	entry.Ext = sanitizeExt(entry.Ext)
	data, err := surveyjson.Marshal(entry)
	if err != nil {
		return err
	}
	return blobcache.WriteFile(dir, hash+".json", data, fetchCacheMaxBytes, nil)
}
