// Package grantedpath holds approved filesystem access outside project roots.
package grantedpath

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/scopedstore"
)

// Mode is the direction a grant permits.
type Mode string

const (
	// ModeRead permits reads only.
	ModeRead Mode = "read"
	// ModeWrite permits writes and reads of the same path.
	ModeWrite Mode = "write"
)

func (m Mode) permits(want Mode) bool { return m == ModeWrite || m == want }

// Grant is filesystem access a person allowed.
type Grant struct {
	// ID is the shared approval identity.
	ID string
	// SourceCheckpointID identifies the rollback checkpoint of this chat-scoped grant.
	SourceCheckpointID string
	// Path is absolute and cleaned.
	Path string
	Mode Mode
	// Tree covers Path and every descendant. Exact grants cover Path only.
	Tree bool
	// ExpiresAt bounds the grant.
	ExpiresAt *time.Time
}

func (g Grant) covers(abs string, mode Mode, now time.Time) bool {
	if !g.Mode.permits(mode) {
		return false
	}
	if g.ExpiresAt != nil && !g.ExpiresAt.After(now) {
		return false
	}
	return CoversPath(g.Path, g.Tree, abs)
}

// CoversPath reports whether grantPath includes abs. A tree grant covers the
// path and every descendant; an exact grant covers only that path.
func CoversPath(grantPath string, tree bool, abs string) bool {
	grantPath = Normalize(grantPath)
	abs = Normalize(abs)
	if grantPath == "" || abs == "" {
		return false
	}
	if abs == grantPath {
		return true
	}
	if !tree {
		return false
	}
	return strings.HasPrefix(abs, grantPath+string(filepath.Separator))
}

// DurableSource returns durable grants for one project.
type DurableSource func(projectID string) []Grant

// Runtime combines session-tree and durable grants.
type Runtime struct {
	mu      sync.RWMutex
	byRoot  scopedstore.Map[map[string]Grant]
	durable DurableSource
}

// NewRuntime constructs an empty runtime.
func NewRuntime() *Runtime { return &Runtime{} }

// SetDurableSource installs the saved-approvals lookup.
func (r *Runtime) SetDurableSource(fn DurableSource) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.durable = fn
}

// Grant installs filesystem access.
func (r *Runtime) Grant(rootSession string, g Grant) (bool, error) {
	rootSession = strings.TrimSpace(rootSession)
	g.ID = strings.TrimSpace(g.ID)
	g.Path = Normalize(g.Path)
	g.SourceCheckpointID = strings.TrimSpace(g.SourceCheckpointID)
	if r == nil || rootSession == "" || g.ID == "" || g.Path == "" || !filepath.IsAbs(g.Path) {
		return false, fmt.Errorf("granted path authority is incomplete")
	}
	if g.Mode != ModeRead && g.Mode != ModeWrite {
		return false, fmt.Errorf("granted path authority has invalid mode %q", g.Mode)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for existingRoot, grants := range r.byRoot.Snapshot() {
		previous, exists := grants[g.ID]
		if !exists {
			continue
		}
		if existingRoot != rootSession || previous.Path != g.Path || previous.Mode != g.Mode || previous.Tree != g.Tree {
			return false, fmt.Errorf("granted path authority id is already installed for another path")
		}
		if previous.ExpiresAt == nil || previous.ExpiresAt.After(now) {
			return false, nil
		}
	}
	set, ok := r.byRoot.Load(rootSession)
	if !ok || set == nil {
		set = map[string]Grant{}
	}
	set[g.ID] = g
	r.byRoot.Store(rootSession, set)
	return true, nil
}

// RevokeInstalledBy drops a grant only when its installing checkpoint still identifies it.
func (r *Runtime) RevokeInstalledBy(rootSession, id, checkpointID string) bool {
	if r == nil || strings.TrimSpace(checkpointID) == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	set, ok := r.byRoot.Load(rootSession)
	if !ok {
		return false
	}
	grant, present := set[id]
	if !present || grant.SourceCheckpointID != strings.TrimSpace(checkpointID) {
		return false
	}
	delete(set, id)
	r.byRoot.Store(rootSession, set)
	return true
}

// RevokeByID drops a grant from every session tree.
func (r *Runtime) RevokeByID(id string) bool {
	if r == nil || strings.TrimSpace(id) == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	revoked := false
	for rootSession, set := range r.byRoot.Snapshot() {
		if _, present := set[id]; !present {
			continue
		}
		delete(set, id)
		r.byRoot.Store(rootSession, set)
		revoked = true
	}
	return revoked
}

// Covers reports live authority for a path and mode.
func (r *Runtime) Covers(rootSession, projectID, abs string, mode Mode) (Grant, bool) {
	if r == nil {
		return Grant{}, false
	}
	clean := Normalize(abs)
	if clean == "" {
		return Grant{}, false
	}
	r.mu.RLock()
	set, _ := r.byRoot.Load(rootSession)
	durable := r.durable
	now := time.Now()
	for _, g := range set {
		if g.covers(clean, mode, now) {
			r.mu.RUnlock()
			return g, true
		}
	}
	r.mu.RUnlock()
	if durable != nil {
		for _, g := range durable(projectID) {
			g.Path = Normalize(g.Path)
			if g.covers(clean, mode, now) {
				return g, true
			}
		}
	}
	return Grant{}, false
}

// List returns the live grants for a session tree, for Saved approvals.
func (r *Runtime) List(rootSession string) []Grant {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	set, ok := r.byRoot.Load(rootSession)
	if !ok {
		r.mu.RUnlock()
		return nil
	}
	now := time.Now()
	out := make([]Grant, 0, len(set))
	for _, g := range set {
		if g.ExpiresAt == nil || g.ExpiresAt.After(now) {
			out = append(out, g)
		}
	}
	r.mu.RUnlock()
	return out
}

// Forget drops every grant for a session tree, on session close.
func (r *Runtime) Forget(rootSession string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byRoot.Delete(rootSession)
}

// Normalize resolves path aliases through the deepest existing ancestor.
func Normalize(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(path)))
}
