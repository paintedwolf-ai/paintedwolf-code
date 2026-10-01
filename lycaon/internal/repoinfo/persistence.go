package repoinfo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
)

type cachedClassification struct {
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Language string    `json:"language"`
}

func orientationCacheKey(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])
}

func (p *fileProvider) cachedBrief(root string) *Brief {
	if p.cacheDir == "" {
		return nil
	}
	var brief Brief
	if !readOrientationCache(filepath.Join(p.cacheDir, orientationCacheKey(root)+".brief.json"), &brief) {
		return nil
	}
	brief.Root = root
	// A restored brief is shown but stays partial until this run measures.
	brief.Materialized = true
	brief.Partial = true
	brief.Refreshing = true
	return &brief
}

func readOrientationCache(path string, target any) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	return json.NewDecoder(io.LimitReader(f, 64<<20)).Decode(target) == nil
}

func (p *fileProvider) loadClassifications(root string) {
	if p.cacheDir == "" {
		return
	}
	p.memo.mu.Lock()
	_, present := p.memo.byRoot[root]
	p.memo.mu.Unlock()
	if present {
		return
	}
	var stored map[string]cachedClassification
	if !readOrientationCache(filepath.Join(p.cacheDir, orientationCacheKey(root)+".languages.json"), &stored) {
		return
	}
	files := make(map[string]classifiedFile, len(stored))
	for path, entry := range stored {
		files[path] = classifiedFile{size: entry.Size, modified: entry.Modified, lang: entry.Language}
	}
	p.memo.replace(root, files)
}

// Large classification maps are rebuilt instead of persisted.
const orientationCacheMaxFiles = 250_000

func (p *fileProvider) persistOrientation(root string, brief *Brief) {
	if p.cacheDir == "" || p.ctx.Err() != nil {
		return
	}
	if err := os.MkdirAll(p.cacheDir, 0o700); err != nil {
		briefLog.Debug("orientation cache directory failed", "err", err)
		return
	}
	p.memo.mu.Lock()
	stored := make(map[string]cachedClassification, len(p.memo.byRoot[root]))
	for path, entry := range p.memo.byRoot[root] {
		stored[path] = cachedClassification{Size: entry.size, Modified: entry.modified, Language: entry.lang}
	}
	p.memo.mu.Unlock()
	values := map[string]any{".brief.json": brief}
	if len(stored) <= orientationCacheMaxFiles {
		values[".languages.json"] = stored
	} else {
		// Removing the old map prevents stale classifications from being restored.
		_ = os.Remove(filepath.Join(p.cacheDir, orientationCacheKey(root)+".languages.json"))
	}
	for suffix, value := range values {
		raw, err := json.Marshal(value)
		if err == nil {
			_, err = fseffect.Replace(fseffect.ReplaceRequest{
				Location: fseffect.Location{Root: p.cacheDir, Rel: orientationCacheKey(root) + suffix},
				Source:   bytes.NewReader(raw), Mode: 0o600,
			})
		}
		if err != nil {
			briefLog.Debug("orientation cache write failed", "project_dir", root, "err", err)
		}
	}
}
