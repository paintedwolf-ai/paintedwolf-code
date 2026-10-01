package modelfeed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
)

// Feed manages the process-wide model catalog document.
type Feed struct {
	mu        sync.RWMutex
	refreshMu sync.Mutex

	url      string
	cacheDir string
	ttl      time.Duration

	doc       *Document
	fetchedAt time.Time

	getBytes func(ctx context.Context, url string) ([]byte, error)

	now func() time.Time

	listeners []func()
}

// AddRefreshListener registers a hook for the feed lifetime.
func (f *Feed) AddRefreshListener(listener func()) {
	if f == nil || listener == nil {
		return
	}
	f.mu.Lock()
	f.listeners = append(f.listeners, listener)
	f.mu.Unlock()
}

// Options configures a Feed. Zero values use production defaults.
type Options struct {
	URL      string
	CacheDir string // default: {UserConfigDir}/modelfeed
	TTL      time.Duration
	// GetBytes overrides the feed fetch boundary for tests.
	GetBytes func(ctx context.Context, url string) ([]byte, error)
}

// New loads the disk cache without fetching the feed.
func New(opts Options) (*Feed, error) {
	url := stringsOr(opts.URL, DefaultURL)
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = CacheTTL
	}
	cacheDir := opts.CacheDir
	if cacheDir == "" {
		root, err := configdir.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("modelfeed: config dir: %w", err)
		}
		cacheDir = enginepaths.ModelfeedRootUnder(root)
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("modelfeed: cache dir: %w", err)
	}
	f := &Feed{
		url:      url,
		cacheDir: cacheDir,
		ttl:      ttl,
		getBytes: opts.GetBytes,
		now:      time.Now,
	}
	if f.getBytes == nil {
		f.getBytes = getFeedBytes
	}
	_ = f.loadDiskUnlocked()
	return f, nil
}

func stringsOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// FetchedAt returns when the in-memory document was last fetched (zero if none).
func (f *Feed) FetchedAt() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.fetchedAt
}

// Status returns ok | stale | unavailable for wire projection.
func (f *Feed) Status() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.statusLocked()
}

func (f *Feed) statusLocked() string {
	if f.doc == nil {
		return StatusUnavailable
	}
	if f.ttl > 0 && f.now().Sub(f.fetchedAt) > f.ttl {
		return StatusStale
	}
	return StatusOK
}

// Usable reports whether a document is available for catalog-authoritative merge.
func (f *Feed) Usable() bool {
	_, _, usable := f.Snapshot()
	return usable
}

// Snapshot returns the in-memory document without network I/O.
func (f *Feed) Snapshot() (doc *Document, status string, usable bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	usable = f.doc != nil && len(f.doc.Providers) > 0
	return f.doc, f.statusLocked(), usable
}

// Document refreshes missing or expired data, retaining cached data on failure.
func (f *Feed) Document(ctx context.Context) (*Document, error) {
	f.mu.RLock()
	doc := f.doc
	fetched := f.fetchedAt
	ttl := f.ttl
	f.mu.RUnlock()

	needRefresh := doc == nil || (ttl > 0 && f.now().Sub(fetched) > ttl)
	if !needRefresh {
		return doc, nil
	}
	refreshed, err := f.Refresh(ctx)
	if err == nil {
		return refreshed, nil
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.doc != nil {
		return f.doc, nil
	}
	return nil, err
}

// Refresh publishes the fetched document after writing it to disk.
func (f *Feed) Refresh(ctx context.Context) (*Document, error) {
	f.refreshMu.Lock()
	defer f.refreshMu.Unlock()
	raw, err := f.getBytes(ctx, f.url)
	if err != nil {
		return nil, fmt.Errorf("modelfeed: refresh: %w", err)
	}
	now := f.now()
	doc, err := parseDocument(raw, now, f.url)
	if err != nil {
		return nil, err
	}
	if err := f.writeDisk(raw, Meta{FetchedAt: now, SourceURL: f.url}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.doc = doc
	f.fetchedAt = now
	listeners := append([]func(){}, f.listeners...)
	f.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
	return doc, nil
}

func (f *Feed) cacheJSONPath() string { return filepath.Join(f.cacheDir, "api.json") }
func (f *Feed) cacheMetaPath() string { return filepath.Join(f.cacheDir, "meta.json") }

func (f *Feed) loadDiskUnlocked() error {
	raw, err := os.ReadFile(f.cacheJSONPath())
	if err != nil {
		return err
	}
	var meta Meta
	if mb, err := os.ReadFile(f.cacheMetaPath()); err == nil {
		_ = json.Unmarshal(mb, &meta)
	}
	fetched := meta.FetchedAt
	if fetched.IsZero() {
		if fi, err := os.Stat(f.cacheJSONPath()); err == nil {
			fetched = fi.ModTime()
		} else {
			fetched = f.now()
		}
	}
	doc, err := parseDocument(raw, fetched, stringsOr(meta.SourceURL, f.url))
	if err != nil {
		return err
	}
	f.doc = doc
	f.fetchedAt = fetched
	return nil
}

func (f *Feed) writeDisk(raw []byte, meta Meta) error {
	if err := os.MkdirAll(f.cacheDir, 0o700); err != nil {
		return err
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(f.cacheJSONPath()),
		Source:   bytes.NewReader(raw),
		Mode:     0o600,
		DirMode:  0o700,
	}); err != nil {
		return err
	}
	mb, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(f.cacheMetaPath()),
		Source:   bytes.NewReader(mb),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}

// EligibleModels returns ConversationEligible models for a feed provider key.
func (d *Document) EligibleModels(feedKey string) []Model {
	if d == nil {
		return nil
	}
	p, ok := d.Providers[feedKey]
	if !ok {
		return nil
	}
	out := make([]Model, 0, len(p.Models))
	for _, m := range p.Models {
		if ConversationEligible(m) {
			out = append(out, m)
		}
	}
	return out
}
