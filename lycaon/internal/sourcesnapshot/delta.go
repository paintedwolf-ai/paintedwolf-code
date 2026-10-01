package sourcesnapshot

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/repochange"
)

// deltaPathLimit bounds retained changes; overflow requires a full survey.
const deltaPathLimit = 65536

// deltaTracker accumulates observed changes between source publications.
type deltaTracker struct {
	mu    sync.Mutex
	roots map[string]*rootDelta
	seq   uint64
	// admit excludes changes outside the capture scope.
	admit func(ctx context.Context, root, rel string) bool
}

type rootDelta struct {
	// covered names the full observation needed for incremental publication.
	covered string
	scopeID string
	// paths holds each changed path with the sequence it arrived at.
	paths map[string]uint64
	// overflow requires a full survey after path loss or scope changes.
	overflow   bool
	overflowAt uint64
}

func newDeltaTracker() *deltaTracker {
	return &deltaTracker{roots: make(map[string]*rootDelta)}
}

func (t *deltaTracker) observe(ctx context.Context, ev repochange.Event) {
	if t == nil || ev.Kind != repochange.WorktreeChanged {
		return
	}
	root := filepath.Clean(ev.ProjectDir)
	// Scope decisions read ignore files; they run before the lock.
	overflow := false
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
		if ignoreFileChanged(rel) {
			overflow = true
		} else if t.admit != nil && !t.admit(ctx, root, rel) {
			continue
		}
		rels = append(rels, rel)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.root(root)
	// Cancellation may narrow admission after the mutation already committed.
	if overflow || ctx.Err() != nil {
		t.seq++
		d.overflow = true
		d.overflowAt = t.seq
	}
	for _, rel := range rels {
		if len(d.paths) >= deltaPathLimit {
			t.seq++
			d.overflow = true
			d.overflowAt = t.seq
			return
		}
		t.seq++
		d.paths[rel] = t.seq
	}
}

func (t *deltaTracker) root(root string) *rootDelta {
	d := t.roots[root]
	if d == nil {
		d = &rootDelta{paths: make(map[string]uint64)}
		t.roots[root] = d
	}
	return d
}

// A publication consumes changes through the plan's sequence.
type deltaPlan struct {
	// covered is the head the changes apply to; empty means survey the root.
	covered string
	scopeID string
	paths   []string
	// upTo leaves later changes pending for the next publication.
	upTo uint64
	// overflow requires a full survey of the candidate.
	overflow bool
}

// Full surveys also record a sequence boundary.
func (t *deltaTracker) sequence() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seq
}

// An empty covered head selects a full survey.
func (t *deltaTracker) begin(root, scopeID string) deltaPlan {
	root = filepath.Clean(root)
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.root(root)
	plan := deltaPlan{upTo: t.seq, scopeID: scopeID}
	if d.covered == "" || d.scopeID != scopeID || d.overflow || !repochange.Coverage(root).Complete {
		// The survey covers existing changes; later events remain pending.
		d.resetLocked()
		return plan
	}
	plan.covered = d.covered
	plan.paths = make([]string, 0, len(d.paths))
	for rel := range d.paths {
		plan.paths = append(plan.paths, rel)
	}
	return plan
}

func (d *rootDelta) resetLocked() {
	d.covered = ""
	d.overflow = false
	clear(d.paths)
}

// moved reports whether changes arrived after the plan was taken.
func (t *deltaTracker) moved(root string, plan deltaPlan) bool {
	root = filepath.Clean(root)
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.root(root)
	if d.overflow {
		return true
	}
	for _, seq := range d.paths {
		if seq > plan.upTo {
			return true
		}
	}
	return false
}

// Later changes extend the current candidate's capture.
func (t *deltaTracker) pending(root string, plan deltaPlan) deltaPlan {
	root = filepath.Clean(root)
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.root(root)
	next := deltaPlan{covered: plan.covered, upTo: t.seq}
	if d.overflow {
		next.covered = ""
		next.overflow = true
		d.resetLocked()
		return next
	}
	for rel, seq := range d.paths {
		if seq > plan.upTo {
			next.paths = append(next.paths, rel)
		}
	}
	return next
}

// Publication consumes observed changes through upTo.
func (t *deltaTracker) coveredBy(root, head, scopeID string, upTo uint64, exact bool) {
	root = filepath.Clean(root)
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.root(root)
	if !exact {
		// An unsettled generation requires a fresh survey.
		d.resetLocked()
		return
	}
	for rel, seq := range d.paths {
		if seq <= upTo {
			delete(d.paths, rel)
		}
	}
	d.covered = head
	d.scopeID = scopeID
	if d.overflowAt <= upTo {
		d.overflow = false
	}
}

// forget drops a root's tracking; a released root starts over.
func (t *deltaTracker) forget(root string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.roots, filepath.Clean(root))
	t.mu.Unlock()
}

// deltaRel bounds an event path under its root as a slash-relative path.
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

// Ignore-file changes invalidate admission decisions below them.
func ignoreFileChanged(rel string) bool {
	base := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		base = rel[i+1:]
	}
	return base == ".gitignore" || base == ".ignore" || rel == ".git/info/exclude"
}
