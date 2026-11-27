package pythondocuments

import (
	"errors"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

// CanonicalKey uniquely indexes a resource within its namespace, root, worker
// branch, and cleaned relative path.
type CanonicalKey struct {
	Namespace pythonruntime.Namespace
	RootID    string
	Branch    string
	RelPath   string
}

// CanonicalKeyOf constructs the CanonicalKey for a CanonicalTarget.
func CanonicalKeyOf(target pythonruntime.CanonicalTarget) CanonicalKey {
	return CanonicalKey{
		Namespace: target.Namespace,
		RootID:    target.RootID,
		Branch:    target.Branch,
		RelPath:   cleanRelPath(target.RelPath),
	}
}

func cleanRelPath(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." || rel == "/" {
		return ""
	}
	cleaned := path.Clean("/" + rel)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}

// PinOrigin distinguishes how the earliest unretired base revision was captured.
type PinOrigin string

const (
	PinOriginRead       PinOrigin = "read"
	PinOriginBlindWrite PinOrigin = "blind_write"
	PinOriginStat       PinOrigin = "stat"
	PinOriginCommit     PinOrigin = "commit"
)

// RevisionPin records the earliest unretired base revision for one canonical
// workspace target.
type RevisionPin struct {
	Target       pythonruntime.CanonicalTarget `json:"target"`
	BaseRevision DocumentRevision              `json:"base_revision"`
	Origin       PinOrigin                     `json:"origin"`
	PinnedAt     time.Time                     `json:"pinned_at"`
	CommitCount  int                           `json:"commit_count,omitempty"`
}

// RevisionPinTable maintains canonical earliest-base revision pins for a
// program execution. Once pinned, later probes and rereads MUST NEVER refresh
// an unretired pin; only an authoritative host commit advances it.
type RevisionPinTable struct {
	mu   sync.Mutex
	pins map[CanonicalKey]RevisionPin
	now  func() time.Time
}

// NewRevisionPinTable creates an empty RevisionPinTable.
func NewRevisionPinTable(now func() time.Time) *RevisionPinTable {
	if now == nil {
		now = time.Now
	}
	return &RevisionPinTable{
		pins: make(map[CanonicalKey]RevisionPin),
		now:  now,
	}
}

// PinEarliest records baseRev for target if no active pin already exists.
// Later probes and rereads are ignored and never refresh the earliest pin.
// Returns the active pin and true if this call installed the initial pin.
func (t *RevisionPinTable) PinEarliest(target pythonruntime.CanonicalTarget, baseRev DocumentRevision, origin PinOrigin) (RevisionPin, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := CanonicalKeyOf(target)
	if existing, ok := t.pins[k]; ok {
		return existing, false
	}
	if baseRev.RootID == "" {
		baseRev.RootID = target.RootID
	}
	if baseRev.Branch == "" {
		baseRev.Branch = target.Branch
	}
	if baseRev.DocumentID == "" && target.DocumentID != "" {
		baseRev.DocumentID = target.DocumentID
	}
	pin := RevisionPin{
		Target:       target,
		BaseRevision: baseRev,
		Origin:       origin,
		PinnedAt:     t.now().UTC(),
	}
	t.pins[k] = pin
	return pin, true
}

// Get returns the active pin for target, if any.
func (t *RevisionPinTable) Get(target pythonruntime.CanonicalTarget) (RevisionPin, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	pin, ok := t.pins[CanonicalKeyOf(target)]
	return pin, ok
}

// AdvanceAfterCommit advances an active pin to the authoritative revision
// returned by a host commit in a continuing context. Returns an error if
// committed lacks sufficient revision identity.
func (t *RevisionPinTable) AdvanceAfterCommit(target pythonruntime.CanonicalTarget, committed DocumentRevision) error {
	if !committed.HasIdentity() {
		return errors.New("cannot advance revision pin without authoritative committed revision identity")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	k := CanonicalKeyOf(target)
	existing := t.pins[k]
	updatedTarget := target
	if committed.DocumentID != "" {
		updatedTarget.DocumentID = committed.DocumentID
	}
	t.pins[k] = RevisionPin{
		Target:       updatedTarget,
		BaseRevision: committed,
		Origin:       PinOriginCommit,
		PinnedAt:     t.now().UTC(),
		CommitCount:  existing.CommitCount + 1,
	}
	return nil
}

// Snapshot returns all active pins sorted deterministically by canonical key.
func (t *RevisionPinTable) Snapshot() []RevisionPin {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]RevisionPin, 0, len(t.pins))
	for _, pin := range t.pins {
		out = append(out, pin)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := CanonicalKeyOf(out[i].Target), CanonicalKeyOf(out[j].Target)
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.RootID != b.RootID {
			return a.RootID < b.RootID
		}
		if a.Branch != b.Branch {
			return a.Branch < b.Branch
		}
		return a.RelPath < b.RelPath
	})
	return out
}
