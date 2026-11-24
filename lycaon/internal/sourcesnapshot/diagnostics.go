package sourcesnapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/repochange"
)

// SurveyReason states why a full root survey ran instead of delta application.
type SurveyReason string

const (
	SurveyNoCoveredHead         SurveyReason = "no_covered_head"
	SurveyOverflow              SurveyReason = "overflow"
	SurveyIncompleteWatcher     SurveyReason = "incomplete_watcher"
	SurveyScopeMismatch         SurveyReason = "scope_mismatch"
	SurveyMissingManifest       SurveyReason = "missing_manifest"
	SurveyExplicitVerifyContent SurveyReason = "explicit_verify_content"
)

// SurveyPhase distinguishes initial surveys from convergence resurveys and confirmations.
type SurveyPhase string

const (
	SurveyPhaseInitial  SurveyPhase = "survey"
	SurveyPhaseResurvey SurveyPhase = "resurvey"
	SurveyPhaseConfirm  SurveyPhase = "confirm"
)

// SurveyEvent records one physical survey start without recording filenames.
type SurveyEvent struct {
	RootsKey     string
	RootCount    int
	RootIdentity string
	RootPath     string
	Verify       Verify
	Epoch        uint64
	Phase        SurveyPhase
	Reason       SurveyReason
}

// SnapshotMetrics counts physical stat, reuse, and content-read work.
type SnapshotMetrics struct {
	Surveys      int64
	ConfirmRuns  int64
	StatChecks   int64
	StatReuses   int64
	ContentReads int64
}

type snapshotDiagnostics struct {
	mu          sync.RWMutex
	nextID      uint64
	observers   map[uint64]func(SurveyEvent)
	surveys     atomic.Int64
	confirms    atomic.Int64
	statChecks  atomic.Int64
	statReuses  atomic.Int64
	contentRead atomic.Int64
}

func (d *snapshotDiagnostics) observe(fn func(SurveyEvent)) func() {
	if fn == nil {
		return func() {}
	}
	d.mu.Lock()
	if d.observers == nil {
		d.observers = make(map[uint64]func(SurveyEvent))
	}
	d.nextID++
	id := d.nextID
	d.observers[id] = fn
	d.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			d.mu.Lock()
			delete(d.observers, id)
			d.mu.Unlock()
		})
	}
}

func (d *snapshotDiagnostics) recordSurvey(ctx context.Context, req Request, root Root, phase SurveyPhase, reason SurveyReason) {
	if phase == SurveyPhaseConfirm {
		d.confirms.Add(1)
	} else {
		d.surveys.Add(1)
	}
	canon := gitrepo.CanonicalDir(root.Path)
	if canon == "" {
		canon = root.Path
	}
	sum := sha256.Sum256([]byte(canon))
	rootID := hex.EncodeToString(sum[:8])
	epoch := repochange.CurrentEpoch(root.Path)
	ev := SurveyEvent{
		RootsKey:     rootsKey(req.Roots),
		RootCount:    len(req.Roots),
		RootIdentity: rootID,
		RootPath:     canon,
		Verify:       req.Verify,
		Epoch:        epoch.Value,
		Phase:        phase,
		Reason:       reason,
	}
	slog.DebugContext(ctx, "source snapshot survey started",
		"roots_key", ev.RootsKey,
		"root_count", ev.RootCount,
		"root_id", ev.RootIdentity,
		"verify", string(ev.Verify),
		"epoch", ev.Epoch,
		"phase", string(ev.Phase),
		"reason", string(ev.Reason),
	)
	d.mu.RLock()
	listeners := make([]func(SurveyEvent), 0, len(d.observers))
	for _, fn := range d.observers {
		listeners = append(listeners, fn)
	}
	d.mu.RUnlock()
	for _, fn := range listeners {
		fn(ev)
	}
}

func (d *snapshotDiagnostics) snapshot() SnapshotMetrics {
	return SnapshotMetrics{
		Surveys:      d.surveys.Load(),
		ConfirmRuns:  d.confirms.Load(),
		StatChecks:   d.statChecks.Load(),
		StatReuses:   d.statReuses.Load(),
		ContentReads: d.contentRead.Load(),
	}
}

// ObserveSurveys subscribes to value-free physical survey start events.
func (s *Store) ObserveSurveys(fn func(SurveyEvent)) func() {
	if s == nil || s.deltas == nil {
		return func() {}
	}
	return s.deltas.diag.observe(fn)
}

// Metrics returns cumulative physical survey, stat, and content-read counts.
func (s *Store) Metrics() SnapshotMetrics {
	if s == nil || s.deltas == nil {
		return SnapshotMetrics{}
	}
	return s.deltas.diag.snapshot()
}
