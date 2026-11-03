package observation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// TrackerConfig configures a visibility, cache coherence, and source observation Tracker.
type TrackerConfig struct {
	ViewID string
	Facts  VisibilityFacts
	Core   *layeredfs.Core
	Roots  []layeredfs.RootSpec
	Clock  func() time.Time
}

type nodeIdentityKey struct {
	nodeID layeredfs.NodeID
	gen    layeredfs.Generation
}

type recordedTrunkState struct {
	kind                           TrunkChangeKind
	sha256                         string
	size, modTimeNS                int64
	ino, dev, generation           uint64
	deleted                        bool
	updatedAt                      time.Time
}

type obsState struct {
	nextObsSeq, sourceGen uint64
	rootGens              map[string]uint64
	observations          []SourceObservation
	obsByPath             map[layeredfs.QualifiedPath][]int
	obsByID               map[string]int
	heldByHandle          map[layeredfs.HandleID]HeldDescriptorObservation
	heldByNode            map[nodeIdentityKey]HeldDescriptorObservation
	negativeLookups       map[layeredfs.QualifiedPath]uint64
}

type coherenceState struct {
	trunkState                     map[layeredfs.QualifiedPath]recordedTrunkState
	staleObsIDs                    map[string]string
	pendingNotice                  map[layeredfs.QualifiedPath]struct{}
	pendingObsRefs                 map[string]struct{}
	lastConsumedNotice             *TrunkChangedNotice
	overflowByRoot                 map[string][]string
}

// DescriptorTracker provides open-descriptor observation methods on a Tracker.
type DescriptorTracker struct{ tr *Tracker }

// CoherenceTracker provides trunk change and invalidation methods on a Tracker.
type CoherenceTracker struct{ tr *Tracker }

// VerificationTracker provides verification receipt binding and freshness evaluation on a Tracker.
type VerificationTracker struct{ tr *Tracker }

// Tracker coordinates trunk visibility, targeted adapter cache invalidation,
// source observation provenance, watcher overflow uncertainty, and verification staleness.
type Tracker struct {
	DescriptorTracker
	CoherenceTracker
	VerificationTracker
	mu                sync.RWMutex
	viewID            string
	facts             VisibilityFacts
	core              *layeredfs.Core
	rootsByID         map[string]layeredfs.RootSpec
	clock             func() time.Time
	store             obsState
	coherence         coherenceState
	invalidationSinks []InvalidationSink
}

var _ layeredfs.ObservationSink = (*Tracker)(nil)

// NewTracker constructs a Tracker with validated adapter visibility facts.
func NewTracker(cfg TrackerConfig) (*Tracker, error) {
	if cfg.ViewID == "" {
		return nil, errors.New("observation: ViewID is required")
	}
	facts := NormalizeVisibilityFacts(cfg.Facts)
	if err := ValidateVisibilityFacts(facts); err != nil {
		return nil, err
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	rootsByID := make(map[string]layeredfs.RootSpec, len(cfg.Roots))
	rootGens := make(map[string]uint64, len(cfg.Roots))
	for _, r := range cfg.Roots {
		if r.ID != "" {
			rootsByID[r.ID] = r
			rootGens[r.ID] = 1
		}
	}
	t := &Tracker{
		viewID:    cfg.ViewID,
		facts:     facts,
		core:      cfg.Core,
		rootsByID: rootsByID,
		clock:     clock,
		store: obsState{
			sourceGen:       1,
			rootGens:        rootGens,
			obsByPath:       make(map[layeredfs.QualifiedPath][]int),
			obsByID:         make(map[string]int),
			heldByHandle:    make(map[layeredfs.HandleID]HeldDescriptorObservation),
			heldByNode:      make(map[nodeIdentityKey]HeldDescriptorObservation),
			negativeLookups: make(map[layeredfs.QualifiedPath]uint64),
		},
		coherence: coherenceState{
			trunkState:     make(map[layeredfs.QualifiedPath]recordedTrunkState),
			staleObsIDs:    make(map[string]string),
			pendingNotice:  make(map[layeredfs.QualifiedPath]struct{}),
			pendingObsRefs: make(map[string]struct{}),
			overflowByRoot: make(map[string][]string),
		},
	}
	t.DescriptorTracker.tr = t
	t.CoherenceTracker.tr = t
	t.VerificationTracker.tr = t
	return t, nil
}

// BindCore attaches a *layeredfs.Core instance and its root specifications to the Tracker.
func (t *Tracker) BindCore(core *layeredfs.Core, roots ...layeredfs.RootSpec) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if core != nil {
		t.core = core
	}
	for _, r := range roots {
		if r.ID != "" {
			t.rootsByID[r.ID] = r
			if t.store.rootGens[r.ID] == 0 {
				t.store.rootGens[r.ID] = t.store.sourceGen
			}
		}
	}
}

// SetVisibilityFacts updates the tracker's adapter visibility and attribution facts.
func (t *Tracker) SetVisibilityFacts(facts VisibilityFacts) error {
	facts = NormalizeVisibilityFacts(facts)
	if err := ValidateVisibilityFacts(facts); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.facts = facts
	for i := range t.store.observations {
		obs := &t.store.observations[i]
		if obs.Layer == layeredfs.LayerTrunk {
			obs.Completeness = effectiveObservationCompleteness(facts.AttributionCompleteness, obs.SHA256 != "", obs.Provenance, obs.Uncertain)
		}
	}
	return nil
}

// BindViewCapabilities updates the tracker's visibility facts from a ViewCapabilityDescriptor.
func (t *Tracker) BindViewCapabilities(desc ViewCapabilityDescriptor) error {
	return t.SetVisibilityFacts(VisibilityFactsFromDescriptor(desc))
}

// Facts returns the declared adapter visibility and cache capability facts.
func (t *Tracker) Facts() VisibilityFacts {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.facts
}

// VisibilityFacts returns the declared adapter visibility and cache capability facts.
func (t *Tracker) VisibilityFacts() VisibilityFacts { return t.Facts() }

// RegisterInvalidationSink registers an adapter sink to receive targeted cache invalidations.
func (t *Tracker) RegisterInvalidationSink(sink InvalidationSink) {
	if sink == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.invalidationSinks = append(t.invalidationSinks, sink)
}

// CurrentSourceGeneration returns the monotonic source generation across the tracker.
func (t *Tracker) CurrentSourceGeneration() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.store.sourceGen
}

// RootSourceGeneration returns the current source generation for a specific root ID.
func (t *Tracker) RootSourceGeneration(rootID string) uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if gen, ok := t.store.rootGens[rootID]; ok {
		return gen
	}
	return t.store.sourceGen
}

// RecordObservation implements layeredfs.ObservationSink for server/core filesystem requests.
func (t *Tracker) RecordObservation(ev layeredfs.ObservationEvent) {
	_ = t.RecordServerFSObservation(ev)
}

// RecordServerFSObservation records a filesystem request observed by the layeredfs server/core.
func (t *Tracker) RecordServerFSObservation(ev layeredfs.ObservationEvent) SourceObservation {
	qpath := normalizeQPath(ev.Path)
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.store.negativeLookups, qpath)
	obs := buildBaseObservationLocked(t, qpath, ev, ProvenanceServerFSRequest, t.facts.AttributionCompleteness)
	return appendObservationLocked(t, obs)
}

// RecordExactNativeObservation records a complete, exact native content observation with SHA256 digest.
func (t *Tracker) RecordExactNativeObservation(
	qpath layeredfs.QualifiedPath,
	data []byte,
	attr layeredfs.EntryAttr,
) SourceObservation {
	qpath = normalizeQPath(qpath)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])

	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.store.negativeLookups, qpath)
	ev := layeredfs.ObservationEvent{
		ViewID:     t.viewID,
		Path:       qpath,
		Layer:      attr.Layer,
		NodeID:     attr.NodeID,
		Generation: attr.Generation,
		Kind:       layeredfs.ObserveRead,
		Size:       int64(len(data)),
		ModTimeNS:  attr.ModTime.UnixNano(),
		SHA256:     digest,
	}
	obs := buildBaseObservationLocked(t, qpath, ev, ProvenanceExactNativeRead, t.facts.AttributionCompleteness)
	obs.SHA256 = digest
	obs.Size = int64(len(data))
	return appendObservationLocked(t, obs)
}

// RecordIndexDerivedObservation records an index-derived inventory observation.
func (t *Tracker) RecordIndexDerivedObservation(qpath layeredfs.QualifiedPath, sha256Hex string, size, modTimeNS int64) SourceObservation {
	qpath = normalizeQPath(qpath)
	t.mu.Lock()
	defer t.mu.Unlock()
	ev := layeredfs.ObservationEvent{
		ViewID: t.viewID, Path: qpath, Layer: layeredfs.LayerTrunk,
		Kind: layeredfs.ObserveStat, Size: size, ModTimeNS: modTimeNS, SHA256: sha256Hex,
	}
	obs := buildBaseObservationLocked(t, qpath, ev, ProvenanceIndexDerived, CompletenessPartialKernelCached)
	return appendObservationLocked(t, obs)
}

// RecordNegativeLookup records that a lookup for qpath returned ENOENT.
func (t *Tracker) RecordNegativeLookup(qpath layeredfs.QualifiedPath) {
	qpath = normalizeQPath(qpath)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.store.negativeLookups[qpath] = rootGenLocked(t, qpath.RootID)
}

// ObserveLookupResult records either a negative lookup on fs.ErrNotExist or a positive lookup observation.
func (t *Tracker) ObserveLookupResult(qpath layeredfs.QualifiedPath, attr layeredfs.EntryAttr, err error) {
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			t.RecordNegativeLookup(qpath)
		}
		return
	}
	t.RecordServerFSObservation(layeredfs.ObservationEvent{
		ViewID:     t.viewID,
		Path:       qpath,
		Layer:      attr.Layer,
		NodeID:     attr.NodeID,
		Generation: attr.Generation,
		Kind:       layeredfs.ObserveLookup,
		Size:       attr.Size,
		ModTimeNS:  attr.ModTime.UnixNano(),
	})
}

// Observations returns a snapshot of all recorded observations in chronological order.
func (t *Tracker) Observations() []SourceObservation {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]SourceObservation, len(t.store.observations))
	copy(out, t.store.observations)
	return out
}

// ObservationsForPath returns all recorded observations for qpath in chronological order.
func (t *Tracker) ObservationsForPath(qpath layeredfs.QualifiedPath) []SourceObservation {
	qpath = normalizeQPath(qpath)
	t.mu.RLock()
	defer t.mu.RUnlock()
	indices := t.store.obsByPath[qpath]
	out := make([]SourceObservation, 0, len(indices))
	for _, idx := range indices {
		out = append(out, t.store.observations[idx])
	}
	return out
}

// LatestObservationForPath returns the most recent observation for qpath, if any.
func (t *Tracker) LatestObservationForPath(qpath layeredfs.QualifiedPath) (SourceObservation, bool) {
	qpath = normalizeQPath(qpath)
	t.mu.RLock()
	defer t.mu.RUnlock()
	indices := t.store.obsByPath[qpath]
	if len(indices) == 0 {
		return SourceObservation{}, false
	}
	return t.store.observations[indices[len(indices)-1]], true
}
