package checkpoint

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/scopedstore"
)

// Capture serializes an anchor's manifest and its first-touch interval.
type Capture struct {
	mu        sync.Mutex
	mutations mutationLedger
	anchors   scopedstore.LRU[string]
}

func (c *Capture) Open(ctx context.Context, store *Store, rootID, anchorID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := store.Open(ctx, rootID, anchorID); err != nil {
		return err
	}
	c.mutations.Clear(rootID)
	c.anchors.Store(rootID, anchorID)
	return nil
}

func (c *Capture) Reset(rootID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.anchors.Delete(rootID)
	c.mutations.Clear(rootID)
}

func (c *Capture) RecordPath(ctx context.Context, store *Store, rootID, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	anchor, _ := c.anchors.Load(rootID)
	if anchor == "" {
		return
	}
	first, overflowed := c.mutations.RecordPrimaryTouch(rootID, path)
	if overflowed {
		_ = store.MarkTruncated(ctx, rootID, anchor)
		return
	}
	if first {
		_ = store.CapturePreImage(ctx, rootID, anchor, path)
	}
}

func (c *Capture) RecordBlueprint(ctx context.Context, store *Store, rootID, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	anchor, _ := c.anchors.Load(rootID)
	if anchor != "" {
		_ = store.CaptureBlueprintBinding(ctx, rootID, anchor, path)
	}
}

func (c *Capture) DropSession(ctx context.Context, store *Store, sessionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return store.DropSession(ctx, sessionID)
}

func (c *Capture) DropAnchors(ctx context.Context, store *Store, rootID string, anchors []string) {
	if store == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, anchor := range anchors {
		_ = store.DropAnchor(ctx, rootID, anchor)
	}
}

// mutationLedger records first touches under Capture.mu, up to MaxPaths per interval.
type mutationLedger struct {
	bySession map[string]map[string]struct{} // sessionID -> primary paths
	// reportedOverflow marks the checkpoint incomplete once per interval.
	reportedOverflow map[string]bool
}

// RecordPrimaryTouch reports whether relPath needs a pre-image.
// overflowed fires once when the checkpoint reaches its path limit.
func (l *mutationLedger) RecordPrimaryTouch(sessionID, relPath string) (first, overflowed bool) {
	if l == nil {
		return false, false
	}
	sessionID = strings.TrimSpace(sessionID)
	relPath = NormalizePath(relPath)
	if sessionID == "" || relPath == "" {
		return false, false
	}
	if l.bySession == nil {
		l.bySession = make(map[string]map[string]struct{})
	}
	if l.reportedOverflow == nil {
		l.reportedOverflow = make(map[string]bool)
	}
	paths := l.bySession[sessionID]
	if paths == nil {
		paths = make(map[string]struct{})
		l.bySession[sessionID] = paths
	}
	if _, dup := paths[relPath]; dup {
		return false, false
	}
	if len(paths) >= MaxPaths {
		if l.reportedOverflow[sessionID] {
			return false, false
		}
		l.reportedOverflow[sessionID] = true
		return false, true
	}
	paths[relPath] = struct{}{}
	return true, false
}

// Clear starts a new capture interval for sessionID.
func (l *mutationLedger) Clear(sessionID string) {
	if l == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	delete(l.bySession, sessionID)
	delete(l.reportedOverflow, sessionID)
}

func NormalizePath(relPath string) string {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return ""
	}
	relPath = filepath.ToSlash(filepath.Clean(relPath))
	if relPath == "." || sandbox.HasParentTraversal(relPath) {
		return ""
	}
	return relPath
}
