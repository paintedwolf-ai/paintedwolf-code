package sourcesnapshot

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/pathfold"
	"github.com/lycaon/lycaon/internal/repochange"
)

const (
	deltaPathLimit            = 65536
	maxScopedManifestsPerRoot = 8
)

type scopedManifest struct {
	covered    string
	boundaries []Boundary
	upTo       uint64
}

// deltaTracker accumulates observed changes between source publications.
type deltaTracker struct {
	mu        sync.Mutex
	roots     map[string]*rootDelta
	seq       uint64
	pathLimit int
	admit     func(ctx context.Context, root string) func(rel string) bool
	diag      snapshotDiagnostics
}

type rootDelta struct {
	scopes     map[string]scopedManifest
	paths      map[string]uint64
	overflow   bool
	overflowAt uint64
	rescopedAt uint64
}

func newDeltaTracker() *deltaTracker {
	return &deltaTracker{roots: make(map[string]*rootDelta), pathLimit: deltaPathLimit}
}

func (t *deltaTracker) observe(ctx context.Context, ev repochange.Event) {
	if t == nil || ev.Kind != repochange.WorktreeChanged {
		return
	}
	root := filepath.Clean(ev.ProjectDir)
	overflow, rescoped := false, false
	var admit func(string) bool
	rels := make([]string, 0, len(ev.Paths))
	for _, raw := range ev.Paths {
		if ctx.Err() != nil {
			overflow = true
			break
		}
		rel, ok := deltaRel(root, raw)
		if !ok {
			overflow = true
			continue
		}
		if governed, ok := ignoreFileDomain(rel); ok {
			rescoped = true
			if governed == "." {
				overflow = true
			}
			rels = append(rels, governed)
			continue
		}
		if t.admit != nil {
			if admit == nil {
				admit = t.admit(ctx, root)
			}
			if !admit(rel) {
				continue
			}
		}
		rels = append(rels, rel)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.root(root)
	if overflow || ctx.Err() != nil {
		t.seq++
		d.overflow = true
		d.overflowAt = t.seq
	}
	for _, rel := range rels {
		t.seq++
		if !d.overflow {
			d.paths[rel] = t.seq
		}
	}
	if rescoped {
		d.rescopedAt = t.seq
	}
	if folded, ok := pathfold.Fold(d.paths, t.pathLimit, laterSequence); ok {
		d.paths = folded
	} else {
		d.overflow = true
		d.overflowAt = t.seq
	}
	if d.overflow {
		clear(d.paths)
		d.overflowAt = t.seq
	}
}

func laterSequence(a, b uint64) uint64 { return max(a, b) }

func (t *deltaTracker) root(root string) *rootDelta {
	d := t.roots[root]
	if d == nil {
		d = &rootDelta{scopes: make(map[string]scopedManifest), paths: make(map[string]uint64)}
		t.roots[root] = d
	}
	return d
}

// A publication consumes changes through the plan's sequence.
type deltaPlan struct {
	covered    string
	boundaries []Boundary
	scopeID    string
	paths      []string
	upTo       uint64
	overflow   bool
	rescope    bool
	reason     SurveyReason
}

func (t *deltaTracker) sequence() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seq
}

// An empty covered head selects a full survey and records why.
func (t *deltaTracker) begin(root, scopeID string) deltaPlan {
	root = filepath.Clean(root)
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.root(root)
	plan := deltaPlan{upTo: t.seq, scopeID: scopeID}
	complete := repochange.Coverage(root).Complete
	cov, hasScope := d.scopes[scopeID]
	if !hasScope || cov.covered == "" {
		switch {
		case !complete && len(d.scopes) > 0:
			plan.reason = SurveyIncompleteWatcher
		case len(d.scopes) > 0:
			plan.reason = SurveyScopeMismatch
		case d.overflow:
			plan.reason = SurveyOverflow
		case !complete:
			plan.reason = SurveyNoCoveredHead
		default:
			plan.reason = SurveyNoCoveredHead
		}
		if len(d.scopes) == 0 {
			d.resetLocked()
		}
		return plan
	}
	if !complete {
		plan.reason = SurveyIncompleteWatcher
		delete(d.scopes, scopeID)
		if len(d.scopes) == 0 {
			d.resetLocked()
		}
		return plan
	}
	if d.overflow && d.overflowAt > cov.upTo {
		plan.reason = SurveyOverflow
		delete(d.scopes, scopeID)
		if len(d.scopes) == 0 {
			d.resetLocked()
		}
		return plan
	}
	plan.covered = cov.covered
	plan.boundaries = append([]Boundary(nil), cov.boundaries...)
	plan.rescope = d.rescopedAt > cov.upTo
	for rel, seq := range d.paths {
		if seq > cov.upTo {
			plan.paths = append(plan.paths, rel)
		}
	}
	return plan
}

func (d *rootDelta) resetLocked() {
	clear(d.scopes)
	d.overflow = false
	d.rescopedAt = 0
	clear(d.paths)
}

func (d *rootDelta) manifestPendingLocked(scopeID, manifestID string) bool {
	if d == nil {
		return true
	}
	cov, ok := d.scopes[scopeID]
	if !ok || cov.covered != manifestID {
		return true
	}
	if d.overflow && d.overflowAt > cov.upTo {
		return true
	}
	if d.rescopedAt > cov.upTo {
		return true
	}
	for _, seq := range d.paths {
		if seq > cov.upTo {
			return true
		}
	}
	return false
}

func (t *deltaTracker) moved(root string, plan deltaPlan) bool {
	root = filepath.Clean(root)
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.root(root)
	if d.overflow && d.overflowAt > plan.upTo {
		return true
	}
	for _, seq := range d.paths {
		if seq > plan.upTo {
			return true
		}
	}
	return false
}

func (t *deltaTracker) pending(root string, plan deltaPlan) deltaPlan {
	root = filepath.Clean(root)
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.root(root)
	next := deltaPlan{covered: plan.covered, boundaries: plan.boundaries, scopeID: plan.scopeID, upTo: t.seq, rescope: d.rescopedAt > plan.upTo}
	if d.overflow && d.overflowAt > plan.upTo {
		next.covered = ""
		next.overflow = true
		next.reason = SurveyOverflow
		delete(d.scopes, plan.scopeID)
		if len(d.scopes) == 0 {
			d.resetLocked()
		}
		return next
	}
	for rel, seq := range d.paths {
		if seq > plan.upTo {
			next.paths = append(next.paths, rel)
		}
	}
	return next
}

func (t *deltaTracker) coveredBy(root, manifest string, boundaries []Boundary, scopeID string, upTo uint64, exact bool) {
	root = filepath.Clean(root)
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.root(root)
	if !exact {
		delete(d.scopes, scopeID)
		if len(d.scopes) == 0 {
			d.resetLocked()
		}
		return
	}
	if _, exists := d.scopes[scopeID]; !exists && len(d.scopes) >= maxScopedManifestsPerRoot {
		var oldestKey string
		oldestUpTo := uint64(math.MaxUint64)
		for k, v := range d.scopes {
			if v.upTo < oldestUpTo {
				oldestKey, oldestUpTo = k, v.upTo
			}
		}
		if oldestKey != "" {
			delete(d.scopes, oldestKey)
		}
	}
	d.scopes[scopeID] = scopedManifest{
		covered:    manifest,
		boundaries: append([]Boundary(nil), boundaries...),
		upTo:       upTo,
	}
	minUpTo := uint64(math.MaxUint64)
	for _, v := range d.scopes {
		if v.upTo < minUpTo {
			minUpTo = v.upTo
		}
	}
	for rel, seq := range d.paths {
		if seq <= minUpTo {
			delete(d.paths, rel)
		}
	}
	if d.overflowAt <= minUpTo {
		d.overflow = false
	}
	if d.rescopedAt <= minUpTo {
		d.rescopedAt = 0
	}
}

func (t *deltaTracker) forget(root string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.roots, filepath.Clean(root))
	t.mu.Unlock()
}

func deltaRel(root, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	abs := filepath.Clean(raw)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, filepath.FromSlash(raw))
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	if rel == "" {
		rel = "."
	}
	return rel, true
}

func ignoreFileDomain(rel string) (string, bool) {
	if rel == ".git/info/exclude" {
		return ".", true
	}
	dir, base := ".", rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		dir, base = rel[:i], rel[i+1:]
	}
	if base == ".gitignore" || base == ".ignore" {
		return dir, true
	}
	return "", false
}
