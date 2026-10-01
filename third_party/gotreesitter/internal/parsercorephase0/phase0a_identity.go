//go:build gts_parsercorephase0 && gts_parsercorephase0a

package parsercorephase0

import (
	"fmt"
	"math"
	"sync"
	"unsafe"
)

const (
	phase0AEnabled = true
	Phase0AEnabled = true
)

// Phase0AAcceptedSelectionCapability is an opaque, run-scoped authentication
// token for one captured current compact head.
type Phase0AAcceptedSelectionCapability struct {
	coreInstance  uint64
	runGeneration uint64
	serial        uint64
	head          Head
}

func CapturePhase0ASelectionCapability(core *Core, head Head) (Phase0AAcceptedSelectionCapability, error) {
	return core.CapturePhase0ASelectionCapability(head)
}

func ObservePhase0AAcceptedSelection(core *Core, capability Phase0AAcceptedSelectionCapability) error {
	return core.ObservePhase0AAcceptedSelection(capability)
}

type CoreRunNamespace struct {
	CoreInstance  uint64
	RunGeneration uint64
}

type AttemptKey struct {
	Run          CoreRunNamespace
	AttemptEpoch uint32
}

type ConstructionEventKey struct {
	Attempt AttemptKey
	Serial  uint64
}

type IncomingEdgeKey struct {
	Event  ConstructionEventKey
	Serial uint64
}

// ConstructionOccurrenceKey identifies one occurrence within an event.
type ConstructionOccurrenceKey struct {
	Event ConstructionEventKey
	Slot  uint32
}

type Phase0AConstructionKind uint8

const (
	Phase0AConstructionOrdinaryTerminal Phase0AConstructionKind = iota + 1
	Phase0AConstructionExtraTerminal
	Phase0AConstructionReductionParent
)

// Phase0ABoundaryInput records the complete canonical-boundary input carried
// by one candidate graph edge. It is an observer value, not a graph locator.
type Phase0ABoundaryInput struct {
	Frontier   uint64
	State      StateID
	ByteOffset uint32
	Checkpoint CheckpointID
	Shifted    bool
}

type Phase0ARollbackCause uint8

const (
	Phase0ARollbackUnknown Phase0ARollbackCause = iota
	Phase0ARollbackReturnedError
	Phase0ARollbackPanic
	Phase0ARollbackSchedulerPoison
)

type Phase0APoisonKind uint8

const (
	Phase0APoisonReturnedError Phase0APoisonKind = iota + 1
	Phase0APoisonPanic
	Phase0APoisonWrongCore
	Phase0APoisonStaleToken
	Phase0APoisonNonTopToken
	Phase0APoisonNestedScheduler
)

type Phase0AMutationKind uint8

const (
	Phase0AMutationEvent Phase0AMutationKind = iota + 1
	// Phase0AMutationEdge is reserved for a terminal occurrence-bound candidate
	// edge. Publication, duplicate, replacement, and selection remain unproven.
	Phase0AMutationEdge
	Phase0AMutationOccurrence
	// Phase0AMutationScaffoldEdge is an identity/lifetime test scaffold. It is
	// excluded from semantic construction and incoming-edge proof.
	Phase0AMutationScaffoldEdge
)

type Phase0AMutationRecord struct {
	Kind                Phase0AMutationKind
	TransactionID       uint64
	Event               ConstructionEventKey
	Edge                IncomingEdgeKey
	Occurrence          ConstructionOccurrenceKey
	Payload             SubtreeID
	Predecessor         NodeID
	ConstructionKind    Phase0AConstructionKind
	RolledBack          bool
	RollbackTransaction uint64
	RollbackCause       Phase0ARollbackCause
}

// Phase0AReductionOccurrenceRecord carries reduction-only boundary evidence
// without enlarging every common mutation row.
type Phase0AReductionOccurrenceRecord struct {
	TransactionID       uint64
	Occurrence          ConstructionOccurrenceKey
	Edge                IncomingEdgeKey
	Boundary            Phase0ABoundaryInput
	RolledBack          bool
	RollbackTransaction uint64
	RollbackCause       Phase0ARollbackCause
}

// Phase0ATrailingExtraMigrationRecord authenticates one re-push of an
// existing extra-terminal construction occurrence. SourceLink and
// SourceLowerLink are physical links from the independently proved pop route;
// Occurrence and Edge are fresh identities under SourceOccurrence.Event.
type Phase0ATrailingExtraMigrationRecord struct {
	TransactionID       uint64
	Route               uint64
	TrailingOrdinal     uint32
	SourceLink          LinkID
	SourceLowerLink     LinkID
	SourceExpression    Phase0AExpressionID
	SourceOccurrence    ConstructionOccurrenceKey
	SourceEdge          IncomingEdgeKey
	TargetLink          LinkID
	TargetOccurrence    ConstructionOccurrenceKey
	TargetEdge          IncomingEdgeKey
	Occurrence          ConstructionOccurrenceKey
	Edge                IncomingEdgeKey
	Boundary            Phase0ABoundaryInput
	Payload             SubtreeID
	Predecessor         NodeID
	ScoreDelta          int64
	Order               uint64
	HasOrder            bool
	RolledBack          bool
	RollbackTransaction uint64
	RollbackCause       Phase0ARollbackCause
}

type Phase0ATransactionFrame struct {
	TransactionID uint64
	ParentID      uint64
	MutationStart uint64
}

type Phase0APoisonRecord struct {
	TransactionID uint64
	Kind          Phase0APoisonKind
}

type Phase0AProofSnapshot struct {
	Namespace CoreRunNamespace
	// Mutations includes explicit scaffold rows. Semantic construction proof
	// must exclude Phase0AMutationScaffoldEdge.
	Mutations               []Phase0AMutationRecord
	ReductionOccurrences    []Phase0AReductionOccurrenceRecord
	TrailingExtraMigrations []Phase0ATrailingExtraMigrationRecord
	Candidates              []Phase0ACandidateRecord
	Expressions             []Phase0AExpressionRecord
	Bindings                []Phase0ALinkBindingRecord
	Transitions             []Phase0ATransitionRecord
	Selectors               []Phase0ASelectorRecord
	SelectorRoutes          []Phase0ASelectorRouteRecord
	PopRoutes               []Phase0APopRouteRecord
	PopRouteLinks           []Phase0APopRouteLinkRecord
	SelectionCapabilities   []Phase0ASelectionCapabilityRecord
	AcceptedSelections      []Phase0AAcceptedSelectionRecord
	AcceptedLinks           []Phase0AAcceptedLinkRecord
	SelectedOccurrences     []Phase0ASelectedOccurrenceRecord
	SelectedOccurrenceTrees []Phase0ASelectedOccurrenceSnapshot
	Frames                  []Phase0ATransactionFrame
	OccurrenceCount         uint64
	OccurrenceBytes         uint64
	FirstPoison             *Phase0APoisonRecord
	Failure                 *Phase0AError
}

type Phase0AErrorKind string

const (
	Phase0AErrorCounterOverflow     Phase0AErrorKind = "counter_overflow"
	Phase0AErrorUnregisteredCore    Phase0AErrorKind = "unregistered_core"
	Phase0AErrorRunActive           Phase0AErrorKind = "run_active"
	Phase0AErrorStaleNamespace      Phase0AErrorKind = "stale_namespace"
	Phase0AErrorInvalidEvent        Phase0AErrorKind = "invalid_event"
	Phase0AErrorInvalidOccurrence   Phase0AErrorKind = "invalid_occurrence"
	Phase0AErrorSessionInactive     Phase0AErrorKind = "session_inactive"
	Phase0AErrorSessionActive       Phase0AErrorKind = "session_active"
	Phase0AErrorSessionHasRuns      Phase0AErrorKind = "session_has_active_runs"
	Phase0AErrorCoreCap             Phase0AErrorKind = "core_cap"
	Phase0AErrorRecordCap           Phase0AErrorKind = "record_cap"
	Phase0AErrorByteCap             Phase0AErrorKind = "byte_cap"
	Phase0AErrorFrameCap            Phase0AErrorKind = "frame_cap"
	Phase0AErrorMutationCap         Phase0AErrorKind = "mutation_cap"
	Phase0AErrorOccurrenceCap       Phase0AErrorKind = "occurrence_cap"
	Phase0AErrorOccurrenceByteCap   Phase0AErrorKind = "occurrence_byte_cap"
	Phase0AErrorTransactionProof    Phase0AErrorKind = "transaction_proof"
	Phase0AErrorAttemptUnproven     Phase0AErrorKind = "attempt_unproven"
	Phase0AErrorUnsupportedProof    Phase0AErrorKind = "unsupported_proof"
	Phase0AErrorMissingReference    Phase0AErrorKind = "missing_reference"
	Phase0AErrorAmbiguousReference  Phase0AErrorKind = "ambiguous_reference"
	Phase0AErrorStaleReference      Phase0AErrorKind = "stale_reference"
	Phase0AErrorRolledBackReference Phase0AErrorKind = "rolled_back_reference"
	Phase0AErrorCyclicReference     Phase0AErrorKind = "cyclic_reference"
	Phase0AErrorCrossBoundary       Phase0AErrorKind = "cross_boundary_identity"
)

type Phase0ACounter string

const (
	Phase0ACounterCoreInstance                Phase0ACounter = "core_instance"
	Phase0ACounterRunGeneration               Phase0ACounter = "run_generation"
	Phase0ACounterEventSerial                 Phase0ACounter = "event_serial"
	Phase0ACounterEdgeSerial                  Phase0ACounter = "edge_serial"
	Phase0ACounterOccurrenceSlot              Phase0ACounter = "occurrence_slot"
	Phase0ACounterSelectionCapability         Phase0ACounter = "selection_capability"
	Phase0ACounterAcceptedSelectionGeneration Phase0ACounter = "accepted_selection_generation"
)

type Phase0AError struct {
	Kind      Phase0AErrorKind
	Counter   Phase0ACounter
	Namespace CoreRunNamespace
	Detail    string
}

func (e *Phase0AError) Error() string {
	if e == nil {
		return "parser-core phase zero A: <nil>"
	}
	if e.Counter != "" {
		return fmt.Sprintf("parser-core phase zero A: %s: %s", e.Kind, e.Counter)
	}
	if e.Detail != "" {
		return fmt.Sprintf("parser-core phase zero A: %s: %s", e.Kind, e.Detail)
	}
	return "parser-core phase zero A: " + string(e.Kind)
}

// Phase0AObserverLimits are fixed for one explicit observer session.
type Phase0AObserverLimits struct {
	MaxCores                   uint64
	MaxRecords                 uint64
	MaxBytes                   uint64
	MaxFrames                  uint64
	MaxMutations               uint64
	MaxOccurrences             uint64
	MaxOccurrenceBytes         uint64
	MaxCandidates              uint64
	MaxExpressions             uint64
	MaxBindings                uint64
	MaxTransitions             uint64
	MaxSelectors               uint64
	MaxSelectorRoutes          uint64
	MaxPopRoutes               uint64
	MaxPopRouteLinks           uint64
	MaxTrailingExtraMigrations uint64
	MaxSelectionCapabilities   uint64
	MaxAcceptedSelections      uint64
	MaxAcceptedLinks           uint64
	MaxSelectedOccurrences     uint64
	MaxSelectedDepth           uint64
	MaxSelectedIndexEntries    uint64
	MaxSelectedIndexBytes      uint64
}

const (
	phase0AEventRecordBytes      uint64 = 40
	phase0AEdgeRecordBytes       uint64 = 48
	phase0AOccurrenceRecordBytes uint64 = 88
	// Reduction-only rows are stored in slice backing arrays. Derive their
	// exact compiled element sizes so MaxBytes cannot undercharge layout drift.
	phase0AReductionRecordBytes uint64 = uint64(unsafe.Sizeof(Phase0AReductionOccurrenceRecord{}))
	phase0AReductionEventBytes  uint64 = uint64(unsafe.Sizeof(phase0AReductionEvent{}))
	phase0AFrameRecordBytes     uint64 = 24
)

type phase0AObserver struct {
	coreInstance         uint64
	runGeneration        uint64
	run                  CoreRunNamespace
	nextEvent            uint64
	nextEdge             uint64
	attempt              uint32
	active               bool
	activeBorrows        uint64
	frames               []Phase0ATransactionFrame
	mutations            []Phase0AMutationRecord
	eventIndex           map[ConstructionEventKey]uint64
	nextOccurrenceSlot   map[ConstructionEventKey]uint32
	edgeIndex            map[IncomingEdgeKey]uint64
	occurrenceIndex      map[ConstructionOccurrenceKey]uint64
	occurrenceCount      uint64
	occurrenceBytes      uint64
	firstPoison          *Phase0APoisonRecord
	failure              *Phase0AError
	pendingRollback      Phase0ARollbackCause
	reduction            phase0AReductionConstruction
	reductionOccurrences []Phase0AReductionOccurrenceRecord
	factor               phase0AFactorObserver
	route                phase0ARouteObserver
}

type phase0AReductionConstruction struct {
	active      bool
	transaction uint64
	expected    uint32
	observed    uint32
	events      []phase0AReductionEvent
	routeStart  uint64
	routeCount  uint32
	routeProof  bool
}

type phase0AReductionEvent struct {
	payload  SubtreeID
	key      ConstructionEventKey
	nextSlot uint32
}

var phase0AObservers = struct {
	sync.Mutex
	nextCoreInstance uint64
	active           bool
	limits           Phase0AObserverLimits
	records          uint64
	bytes            uint64
	failure          *Phase0AError
	byCore           map[*Core]*phase0AObserver
}{
	byCore: make(map[*Core]*phase0AObserver),
}

// BeginPhase0AObserverSession starts the tag-only observer lifetime.
func BeginPhase0AObserverSession(limits Phase0AObserverLimits) error {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	if phase0AObservers.active {
		return &Phase0AError{Kind: Phase0AErrorSessionActive}
	}
	phase0AObservers.active = true
	phase0AObservers.limits = limits
	phase0AObservers.records = 0
	phase0AObservers.bytes = 0
	phase0AObservers.failure = nil
	phase0AObservers.byCore = make(map[*Core]*phase0AObserver)
	return nil
}

// EndPhase0AObserverSession clears all pointer identity after proving no run is active.
func EndPhase0AObserverSession() error {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	if !phase0AObservers.active {
		return &Phase0AError{Kind: Phase0AErrorSessionInactive}
	}
	for _, observer := range phase0AObservers.byCore {
		if observer.active {
			return &Phase0AError{Kind: Phase0AErrorSessionHasRuns, Namespace: observer.run}
		}
	}
	phase0AObservers.active = false
	phase0AObservers.limits = Phase0AObserverLimits{}
	phase0AObservers.records = 0
	phase0AObservers.bytes = 0
	phase0AObservers.failure = nil
	phase0AObservers.byCore = nil
	return nil
}

func phase0AInvalidateCore(core *Core) error {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	if observer := phase0AObservers.byCore[core]; observer != nil {
		if observer.activeBorrows != 0 {
			return &Phase0AError{Kind: Phase0AErrorRunActive, Namespace: observer.run, Detail: "reset while selected occurrence views are borrowed"}
		}
		observer.active = false
		observer.frames = nil
		observer.mutations = nil
		observer.eventIndex = nil
		observer.nextOccurrenceSlot = nil
		observer.edgeIndex = nil
		observer.occurrenceIndex = nil
		observer.occurrenceCount = 0
		observer.occurrenceBytes = 0
		observer.firstPoison = nil
		observer.failure = nil
		observer.pendingRollback = Phase0ARollbackUnknown
		observer.reduction = phase0AReductionConstruction{}
		observer.reductionOccurrences = nil
		observer.factor = phase0AFactorObserver{}
		observer.route = phase0ARouteObserver{}
	}
	return nil
}

func (c *Core) BeginRun() (CoreRunNamespace, error) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	if !phase0AObservers.active {
		return CoreRunNamespace{}, &Phase0AError{Kind: Phase0AErrorSessionInactive}
	}
	if phase0AObservers.failure != nil {
		copy := *phase0AObservers.failure
		return CoreRunNamespace{}, &copy
	}
	if c == nil {
		return CoreRunNamespace{}, &Phase0AError{Kind: Phase0AErrorUnregisteredCore, Detail: "nil core"}
	}
	observer := phase0AObservers.byCore[c]
	if observer == nil {
		if uint64(len(phase0AObservers.byCore)) >= phase0AObservers.limits.MaxCores {
			return CoreRunNamespace{}, &Phase0AError{Kind: Phase0AErrorCoreCap}
		}
		if phase0AObservers.nextCoreInstance == math.MaxUint64 {
			err := &Phase0AError{Kind: Phase0AErrorCounterOverflow, Counter: Phase0ACounterCoreInstance}
			copy := *err
			phase0AObservers.failure = &copy
			return CoreRunNamespace{}, err
		}
		phase0AObservers.nextCoreInstance++
		observer = &phase0AObserver{coreInstance: phase0AObservers.nextCoreInstance}
		phase0AObservers.byCore[c] = observer
	}
	if observer.activeBorrows != 0 {
		return CoreRunNamespace{}, &Phase0AError{Kind: Phase0AErrorRunActive, Namespace: observer.run, Detail: "begin run while selected occurrence views are borrowed"}
	}
	if observer.active {
		return CoreRunNamespace{}, &Phase0AError{Kind: Phase0AErrorRunActive, Namespace: observer.run}
	}
	if observer.failure != nil {
		return CoreRunNamespace{}, phase0AExistingFailureLocked(observer)
	}
	if observer.runGeneration == math.MaxUint64 {
		return CoreRunNamespace{}, phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorCounterOverflow, Counter: Phase0ACounterRunGeneration, Namespace: observer.run})
	}
	observer.runGeneration++
	observer.run = CoreRunNamespace{CoreInstance: observer.coreInstance, RunGeneration: observer.runGeneration}
	observer.nextEvent, observer.nextEdge, observer.attempt, observer.active = 0, 0, 1, true
	observer.activeBorrows = 0
	observer.frames = nil
	observer.mutations = nil
	observer.eventIndex = make(map[ConstructionEventKey]uint64)
	observer.nextOccurrenceSlot = make(map[ConstructionEventKey]uint32)
	observer.edgeIndex = make(map[IncomingEdgeKey]uint64)
	observer.occurrenceIndex = make(map[ConstructionOccurrenceKey]uint64)
	observer.occurrenceCount = 0
	observer.occurrenceBytes = 0
	observer.firstPoison = nil
	observer.failure = nil
	observer.pendingRollback = Phase0ARollbackUnknown
	observer.reduction = phase0AReductionConstruction{}
	observer.reductionOccurrences = nil
	observer.factor = phase0AFactorObserver{}
	observer.route = phase0ARouteObserver{}
	return observer.run, nil
}

func (c *Core) EndRun(namespace CoreRunNamespace) error {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer, err := phase0AObserverForRunLocked(c, namespace)
	if err != nil {
		return err
	}
	if !observer.active {
		if observer.failure != nil {
			return phase0AExistingFailureLocked(observer)
		}
		return &Phase0AError{Kind: Phase0AErrorStaleNamespace, Namespace: namespace}
	}
	if observer.activeBorrows != 0 {
		return &Phase0AError{Kind: Phase0AErrorRunActive, Namespace: namespace, Detail: "end run while selected occurrence views are borrowed"}
	}
	if len(c.transactions) != 0 || c.schedulerFrame.active {
		if observer.failure != nil {
			return phase0AExistingFailureLocked(observer)
		}
		return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: namespace, Detail: "end run with active parser transaction"})
	}
	if len(observer.frames) != 0 {
		return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: namespace, Detail: "end run with active transaction"})
	}
	if observer.reduction.active {
		return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: namespace, Detail: "end run with active reduction construction"})
	}
	if len(observer.factor.mergePlans) != 0 || len(observer.factor.replacements) != 0 || len(observer.factor.sidecarFrames) != 0 || len(observer.factor.candidateUndos) != 0 {
		return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: namespace, Detail: "end run with active factor provenance scope"})
	}
	if len(observer.route.frames) != 0 {
		return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: namespace, Detail: "end run with active route provenance scope"})
	}
	for _, candidate := range observer.factor.candidates {
		if !candidate.RolledBack && !candidate.Resolved {
			return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorUnsupportedProof, Namespace: namespace, Detail: "end run with unresolved construction candidate"})
		}
	}
	observer.active = false
	if observer.failure != nil {
		return phase0AExistingFailureLocked(observer)
	}
	return nil
}

// phase0AAttempt returns attempt 1 and rejects unsupported retry claims.
func phase0AAttempt(c *Core, namespace CoreRunNamespace, epoch uint32) (AttemptKey, error) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer, err := phase0AObserverForNamespaceLocked(c, namespace)
	if err != nil {
		return AttemptKey{}, err
	}
	if epoch != observer.attempt || epoch != 1 {
		return AttemptKey{}, phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorAttemptUnproven, Namespace: namespace})
	}
	return AttemptKey{Run: namespace, AttemptEpoch: 1}, nil
}

func phase0ANextConstructionEvent(c *Core, namespace CoreRunNamespace) (ConstructionEventKey, error) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer, err := phase0AObserverForNamespaceLocked(c, namespace)
	if err != nil {
		return ConstructionEventKey{}, err
	}
	if observer.attempt != 1 {
		return ConstructionEventKey{}, phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorAttemptUnproven, Namespace: namespace})
	}
	if observer.nextEvent == math.MaxUint64 {
		return ConstructionEventKey{}, phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorCounterOverflow, Counter: Phase0ACounterEventSerial, Namespace: namespace})
	}
	if err := phase0AReserveMutationLocked(observer, phase0AEventRecordBytes); err != nil {
		return ConstructionEventKey{}, err
	}
	observer.nextEvent++
	event := ConstructionEventKey{Attempt: AttemptKey{Run: namespace, AttemptEpoch: 1}, Serial: observer.nextEvent}
	record := Phase0AMutationRecord{Kind: Phase0AMutationEvent, Event: event, TransactionID: phase0ACurrentTransaction(observer)}
	observer.eventIndex[event] = uint64(len(observer.mutations))
	observer.mutations = append(observer.mutations, record)
	return event, nil
}

// phase0ANextScaffoldEdge exercises edge serial and transaction lifetime only.
// It never authenticates a semantic construction occurrence or candidate edge.
func phase0ANextScaffoldEdge(c *Core, event ConstructionEventKey) (IncomingEdgeKey, error) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer, err := phase0AObserverForNamespaceLocked(c, event.Attempt.Run)
	if err != nil {
		return IncomingEdgeKey{}, err
	}
	if event.Attempt.AttemptEpoch != 1 {
		return IncomingEdgeKey{}, phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorAttemptUnproven, Namespace: observer.run})
	}
	index, ok := observer.eventIndex[event]
	if !ok || index >= uint64(len(observer.mutations)) {
		return IncomingEdgeKey{}, phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidEvent, Namespace: observer.run, Detail: "event is not indexed"})
	}
	eventRecord := observer.mutations[index]
	if eventRecord.Kind != Phase0AMutationEvent || eventRecord.Event != event {
		return IncomingEdgeKey{}, phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidEvent, Namespace: observer.run, Detail: "event index mismatch"})
	}
	if eventRecord.RolledBack {
		return IncomingEdgeKey{}, phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidEvent, Namespace: observer.run, Detail: "event was rolled back"})
	}
	if observer.nextEdge == math.MaxUint64 {
		return IncomingEdgeKey{}, phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorCounterOverflow, Counter: Phase0ACounterEdgeSerial, Namespace: observer.run})
	}
	if err := phase0AReserveMutationLocked(observer, phase0AEdgeRecordBytes); err != nil {
		return IncomingEdgeKey{}, err
	}
	observer.nextEdge++
	edge := IncomingEdgeKey{Event: event, Serial: observer.nextEdge}
	record := Phase0AMutationRecord{Kind: Phase0AMutationScaffoldEdge, Event: event, Edge: edge, TransactionID: phase0ACurrentTransaction(observer)}
	observer.edgeIndex[edge] = uint64(len(observer.mutations))
	observer.mutations = append(observer.mutations, record)
	return edge, nil
}

func phase0AObserveTerminalShift(core *Core, payload SubtreeID, predecessor NodeID, target StateID, endByte uint32, shifted, extra bool, scoreDelta int64, order ForkOrder) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer := phase0AObservers.byCore[core]
	if observer == nil || !observer.active || observer.failure != nil {
		return
	}
	kind := Phase0AConstructionOrdinaryTerminal
	if extra {
		kind = Phase0AConstructionExtraTerminal
	}
	phase0AObserveTerminalConstructionLocked(core, observer, payload, kind, 1, func(uint64) NodeID {
		return predecessor
	}, func(uint64) boundaryKey {
		key := core.boundaryKey(target, endByte)
		key.shifted = shifted
		return key
	}, func(uint64) int64 { return scoreDelta }, func(uint64) ForkOrder { return order })
}

func phase0AObserveTerminalCohortShift(core *Core, payload SubtreeID, boundaries []ClassifiedBoundary, targets []StateID, endByte uint32, extra bool) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer := phase0AObservers.byCore[core]
	if observer == nil || !observer.active || observer.failure != nil {
		return
	}
	count := uint64(len(boundaries))
	if count == 0 || len(targets) != len(boundaries) {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: observer.run, Detail: "terminal cohort has no occurrence"})
		return
	}
	if count > math.MaxUint32 {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorCounterOverflow, Counter: Phase0ACounterOccurrenceSlot, Namespace: observer.run})
		return
	}
	for _, boundary := range boundaries {
		if boundary.owner != core {
			phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: observer.run, Detail: "terminal cohort boundary belongs to another core"})
			return
		}
	}
	kind := Phase0AConstructionOrdinaryTerminal
	if extra {
		kind = Phase0AConstructionExtraTerminal
	}
	phase0AObserveTerminalConstructionLocked(core, observer, payload, kind, count, func(index uint64) NodeID {
		return boundaries[index].head.Node
	}, func(index uint64) boundaryKey {
		return core.shiftedBoundaryKey(targets[index], endByte)
	}, func(uint64) int64 { return 0 }, func(uint64) ForkOrder { return ForkOrder{} })
}

func phase0AObserveTerminalConstructionLocked(core *Core, observer *phase0AObserver, payload SubtreeID, kind Phase0AConstructionKind, count uint64, predecessor func(uint64) NodeID, boundary func(uint64) boundaryKey, scoreDelta func(uint64) int64, order func(uint64) ForkOrder) {
	if count > math.MaxUint32 {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorCounterOverflow, Counter: Phase0ACounterOccurrenceSlot, Namespace: observer.run})
		return
	}
	if payload == 0 || predecessor == nil || boundary == nil || scoreDelta == nil || order == nil || count == 0 {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: observer.run, Detail: "invalid terminal construction input"})
		return
	}
	subtree, err := core.subtree(payload)
	if err != nil || !subtree.terminal {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: observer.run, Detail: "terminal construction payload is not an existing terminal"})
		return
	}
	wantExtra := kind == Phase0AConstructionExtraTerminal
	if (kind != Phase0AConstructionOrdinaryTerminal && kind != Phase0AConstructionExtraTerminal) || subtree.extra != wantExtra {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: observer.run, Detail: "terminal construction payload kind mismatch"})
		return
	}
	for index := uint64(0); index < count; index++ {
		if _, err := core.node(predecessor(index)); err != nil {
			phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: observer.run, Detail: "terminal construction predecessor does not exist"})
			return
		}
	}
	if observer.attempt != 1 {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorAttemptUnproven, Namespace: observer.run})
		return
	}
	if observer.nextEvent == math.MaxUint64 {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorCounterOverflow, Counter: Phase0ACounterEventSerial, Namespace: observer.run})
		return
	}
	if observer.nextEdge > math.MaxUint64-count {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorCounterOverflow, Counter: Phase0ACounterEdgeSerial, Namespace: observer.run})
		return
	}
	mutationCount := 1 + 2*count
	occurrenceBytes := count * (phase0AOccurrenceRecordBytes + phase0AEdgeRecordBytes)
	totalBytes := phase0AEventRecordBytes + occurrenceBytes
	if err := phase0AReserveConstructionLocked(observer, mutationCount, count, totalBytes, occurrenceBytes, count); err != nil {
		return
	}

	observer.nextEvent++
	event := ConstructionEventKey{
		Attempt: AttemptKey{Run: observer.run, AttemptEpoch: 1},
		Serial:  observer.nextEvent,
	}
	transaction := phase0ACurrentTransaction(observer)
	eventRecord := Phase0AMutationRecord{
		Kind: Phase0AMutationEvent, TransactionID: transaction,
		Event: event, Payload: payload, ConstructionKind: kind,
	}
	observer.eventIndex[event] = uint64(len(observer.mutations))
	observer.mutations = append(observer.mutations, eventRecord)
	observer.nextOccurrenceSlot[event] = uint32(count)

	for index := uint64(0); index < count; index++ {
		observer.nextEdge++
		occurrence := ConstructionOccurrenceKey{Event: event, Slot: uint32(index + 1)}
		edge := IncomingEdgeKey{Event: event, Serial: observer.nextEdge}
		record := Phase0AMutationRecord{
			TransactionID: transaction, Event: event, Edge: edge, Occurrence: occurrence,
			Payload: payload, Predecessor: predecessor(index), ConstructionKind: kind,
		}
		record.Kind = Phase0AMutationOccurrence
		observer.occurrenceIndex[occurrence] = uint64(len(observer.mutations))
		observer.mutations = append(observer.mutations, record)
		record.Kind = Phase0AMutationEdge
		observer.edgeIndex[edge] = uint64(len(observer.mutations))
		observer.mutations = append(observer.mutations, record)
		phase0AAppendCandidatePrechargedLocked(observer, occurrence, edge, boundary(index), linkInput{prev: predecessor(index), payload: payload, scoreDelta: scoreDelta(index), order: order(index)})
	}
}

func phase0ABoundaryInput(key boundaryKey) Phase0ABoundaryInput {
	return Phase0ABoundaryInput{
		Frontier: key.frontier, State: key.state, ByteOffset: key.byteOffset,
		Checkpoint: key.checkpoint, Shifted: key.shifted,
	}
}

// phase0ABeginReductionConstruction starts one exact reduction-call grouping.
// Records are still allocated by the per-path hook before graph mutation.
func phase0ABeginReductionConstruction(core *Core, count uint64) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer := phase0AObservers.byCore[core]
	if observer == nil || !observer.active || observer.failure != nil {
		return
	}
	if observer.reduction.active {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: observer.run, Detail: "nested reduction construction"})
		return
	}
	if count == 0 {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: observer.run, Detail: "reduction has no pop-path occurrence"})
		return
	}
	if count > math.MaxUint32 {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorCounterOverflow, Counter: Phase0ACounterOccurrenceSlot, Namespace: observer.run})
		return
	}
	if observer.attempt != 1 {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorAttemptUnproven, Namespace: observer.run})
		return
	}
	transaction := phase0ACurrentTransaction(observer)
	if transaction == 0 {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: observer.run, Detail: "reduction construction is outside a transaction"})
		return
	}
	observer.reduction = phase0AReductionConstruction{
		active: true, transaction: transaction, expected: uint32(count),
	}
}

// phase0AObserveReductionOccurrence allocates a fresh occurrence and incoming
// candidate edge before the corresponding graph mutation. Batch-parent reuse
// shares only the construction event for the same payload in this call.
func phase0AObserveReductionOccurrence(core *Core, in linkInput, boundary boundaryKey) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer := phase0AObservers.byCore[core]
	if observer == nil || !observer.active || observer.failure != nil {
		return
	}
	pending := &observer.reduction
	if !pending.active || pending.observed >= pending.expected {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: observer.run, Detail: "reduction occurrence exceeds declared pop paths"})
		return
	}
	if phase0ACurrentTransaction(observer) != pending.transaction {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: observer.run, Detail: "reduction occurrence crossed transaction"})
		return
	}
	subtree, err := core.subtree(in.payload)
	if err != nil || subtree.terminal || subtree.extra {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: observer.run, Detail: "reduction occurrence payload is not an existing parent"})
		return
	}
	if _, err := core.node(in.prev); err != nil {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: observer.run, Detail: "reduction occurrence predecessor does not exist"})
		return
	}
	if boundary.frontier != core.frontier || boundary.checkpoint != core.checkpoint || boundary.shifted || boundary.state == 0 || boundary.byteOffset != subtree.endByte {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: observer.run, Detail: "reduction occurrence boundary input mismatch"})
		return
	}
	eventIndex := -1
	for index := range pending.events {
		if pending.events[index].payload == in.payload {
			eventIndex = index
			break
		}
	}
	seen := eventIndex >= 0
	eventState := phase0AReductionEvent{payload: in.payload}
	if seen {
		eventState = pending.events[eventIndex]
	}
	if !seen && observer.nextEvent == math.MaxUint64 {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorCounterOverflow, Counter: Phase0ACounterEventSerial, Namespace: observer.run})
		return
	}
	if observer.nextEdge == math.MaxUint64 {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorCounterOverflow, Counter: Phase0ACounterEdgeSerial, Namespace: observer.run})
		return
	}
	if seen && eventState.nextSlot == math.MaxUint32 {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorCounterOverflow, Counter: Phase0ACounterOccurrenceSlot, Namespace: observer.run})
		return
	}
	mutationCount := uint64(2)
	mutationBytes := phase0AOccurrenceRecordBytes + phase0AEdgeRecordBytes
	if !seen {
		mutationCount++
		mutationBytes += phase0AEventRecordBytes
	}
	if err := phase0AReserveReductionConstructionLocked(observer, mutationCount, mutationBytes, !seen, 1); err != nil {
		return
	}
	if !seen {
		observer.nextEvent++
		eventState.key = ConstructionEventKey{Attempt: AttemptKey{Run: observer.run, AttemptEpoch: 1}, Serial: observer.nextEvent}
		eventRecord := Phase0AMutationRecord{
			Kind: Phase0AMutationEvent, TransactionID: pending.transaction,
			Event: eventState.key, Payload: in.payload, ConstructionKind: Phase0AConstructionReductionParent,
		}
		observer.eventIndex[eventState.key] = uint64(len(observer.mutations))
		observer.mutations = append(observer.mutations, eventRecord)
	}
	eventState.nextSlot++
	observer.nextEdge++
	occurrence := ConstructionOccurrenceKey{Event: eventState.key, Slot: eventState.nextSlot}
	edge := IncomingEdgeKey{Event: eventState.key, Serial: observer.nextEdge}
	record := Phase0AMutationRecord{
		TransactionID: pending.transaction, Event: eventState.key, Edge: edge, Occurrence: occurrence,
		Payload: in.payload, Predecessor: in.prev, ConstructionKind: Phase0AConstructionReductionParent,
	}
	record.Kind = Phase0AMutationOccurrence
	observer.occurrenceIndex[occurrence] = uint64(len(observer.mutations))
	observer.mutations = append(observer.mutations, record)
	record.Kind = Phase0AMutationEdge
	observer.edgeIndex[edge] = uint64(len(observer.mutations))
	observer.mutations = append(observer.mutations, record)
	observer.reductionOccurrences = append(observer.reductionOccurrences, Phase0AReductionOccurrenceRecord{
		TransactionID: pending.transaction, Occurrence: occurrence, Edge: edge, Boundary: phase0ABoundaryInput(boundary),
	})
	observer.nextOccurrenceSlot[eventState.key] = eventState.nextSlot
	phase0AAppendCandidatePrechargedLocked(observer, occurrence, edge, boundary, in)
	if pending.routeProof && !phase0ABindReductionRouteLocked(observer, pending, occurrence, edge, in.prev) {
		return
	}
	if seen {
		pending.events[eventIndex] = eventState
	} else {
		pending.events = append(pending.events, eventState)
	}
	pending.observed++
}

func phase0AFinishReductionConstruction(core *Core) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer := phase0AObservers.byCore[core]
	if observer == nil || !observer.active {
		return
	}
	pending := observer.reduction
	observer.reduction = phase0AReductionConstruction{}
	if observer.failure != nil {
		return
	}
	if !pending.active || phase0ACurrentTransaction(observer) != pending.transaction {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: observer.run, Detail: "reduction construction finish transaction mismatch"})
		return
	}
	if pending.observed != pending.expected {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: observer.run, Detail: "successful reduction did not observe every pop path"})
		return
	}
	if pending.routeProof && pending.routeCount != pending.expected {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorMissingReference, Namespace: observer.run, Detail: "successful reduction did not prove every physical pop route"})
	}
}

func phase0AReserveReductionConstructionLocked(observer *phase0AObserver, mutationCount, mutationBytes uint64, newEvent bool, candidateCount uint64) error {
	mutationLimit := phase0AObservers.limits.MaxMutations
	if mutationLimit == 0 {
		mutationLimit = math.MaxUint64
	}
	if uint64(len(observer.mutations)) > mutationLimit || mutationCount > mutationLimit-uint64(len(observer.mutations)) {
		return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorMutationCap, Namespace: observer.run})
	}
	occurrenceLimit := phase0AObservers.limits.MaxOccurrences
	if occurrenceLimit == 0 {
		occurrenceLimit = math.MaxUint64
	}
	if observer.occurrenceCount >= occurrenceLimit {
		return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorOccurrenceCap, Namespace: observer.run})
	}
	occurrenceBytes := phase0AOccurrenceRecordBytes + phase0AEdgeRecordBytes
	occurrenceByteLimit := phase0AObservers.limits.MaxOccurrenceBytes
	if occurrenceByteLimit == 0 {
		occurrenceByteLimit = math.MaxUint64
	}
	if observer.occurrenceBytes > occurrenceByteLimit || occurrenceBytes > occurrenceByteLimit-observer.occurrenceBytes {
		return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorOccurrenceByteCap, Namespace: observer.run})
	}
	if err := phase0ACheckFactorRowsLocked(observer, candidateCount, 0, 0, 0, 0, 0); err != nil {
		return err
	}
	recordCount := mutationCount + 1 + candidateCount
	totalBytes := mutationBytes + phase0AReductionRecordBytes + candidateCount*phase0ACandidateBytes
	if newEvent {
		recordCount++
		totalBytes += phase0AReductionEventBytes
	}
	if err := phase0AChargeManyLocked(recordCount, totalBytes); err != nil {
		return phase0AStickyLocked(observer, err.(*Phase0AError))
	}
	observer.occurrenceCount++
	observer.occurrenceBytes += occurrenceBytes
	return nil
}

func phase0AReserveConstructionLocked(observer *phase0AObserver, mutationCount, occurrenceCount, totalBytes, occurrenceBytes, candidateCount uint64) error {
	mutationLimit := phase0AObservers.limits.MaxMutations
	if mutationLimit == 0 {
		mutationLimit = math.MaxUint64
	}
	if uint64(len(observer.mutations)) > mutationLimit || mutationCount > mutationLimit-uint64(len(observer.mutations)) {
		return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorMutationCap, Namespace: observer.run})
	}
	occurrenceLimit := phase0AObservers.limits.MaxOccurrences
	if occurrenceLimit == 0 {
		occurrenceLimit = math.MaxUint64
	}
	if observer.occurrenceCount > occurrenceLimit || occurrenceCount > occurrenceLimit-observer.occurrenceCount {
		return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorOccurrenceCap, Namespace: observer.run})
	}
	occurrenceByteLimit := phase0AObservers.limits.MaxOccurrenceBytes
	if occurrenceByteLimit == 0 {
		occurrenceByteLimit = math.MaxUint64
	}
	if observer.occurrenceBytes > occurrenceByteLimit || occurrenceBytes > occurrenceByteLimit-observer.occurrenceBytes {
		return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorOccurrenceByteCap, Namespace: observer.run})
	}
	if err := phase0ACheckFactorRowsLocked(observer, candidateCount, 0, 0, 0, 0, 0); err != nil {
		return err
	}
	if err := phase0AChargeManyLocked(mutationCount+candidateCount, totalBytes+candidateCount*phase0ACandidateBytes); err != nil {
		return phase0AStickyLocked(observer, err.(*Phase0AError))
	}
	observer.occurrenceCount += occurrenceCount
	observer.occurrenceBytes += occurrenceBytes
	return nil
}

// Phase0AResolveCommittedConstructionOccurrence returns only a construction
// attempt that survived transaction rollback. It does not prove that condense
// published, retained, or selected the attempt's candidate graph edge.
func Phase0AResolveCommittedConstructionOccurrence(core *Core, namespace CoreRunNamespace, key ConstructionOccurrenceKey) (Phase0AMutationRecord, error) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer, err := phase0AObserverForNamespaceLocked(core, namespace)
	if err != nil {
		return Phase0AMutationRecord{}, err
	}
	index, ok := observer.occurrenceIndex[key]
	if !ok || index >= uint64(len(observer.mutations)) {
		return Phase0AMutationRecord{}, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: namespace, Detail: "occurrence is not indexed"}
	}
	record := observer.mutations[index]
	if record.Kind != Phase0AMutationOccurrence || record.Occurrence != key || record.RolledBack {
		return Phase0AMutationRecord{}, &Phase0AError{Kind: Phase0AErrorInvalidOccurrence, Namespace: namespace, Detail: "occurrence did not survive transaction rollback"}
	}
	return record, nil
}

func phase0AReserveMutationLocked(observer *phase0AObserver, bytes uint64) error {
	limit := phase0AObservers.limits.MaxMutations
	if limit == 0 {
		limit = math.MaxUint64
	}
	if uint64(len(observer.mutations)) >= limit {
		return phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorMutationCap, Namespace: observer.run})
	}
	if err := phase0AChargeLocked(bytes); err != nil {
		return phase0AStickyLocked(observer, err.(*Phase0AError))
	}
	return nil
}

func phase0AChargeLocked(bytes uint64) error {
	return phase0AChargeManyLocked(1, bytes)
}

func phase0AChargeManyLocked(records, bytes uint64) error {
	if phase0AObservers.records > phase0AObservers.limits.MaxRecords ||
		records > phase0AObservers.limits.MaxRecords-phase0AObservers.records {
		return &Phase0AError{Kind: Phase0AErrorRecordCap}
	}
	if phase0AObservers.bytes > phase0AObservers.limits.MaxBytes ||
		bytes > phase0AObservers.limits.MaxBytes-phase0AObservers.bytes {
		return &Phase0AError{Kind: Phase0AErrorByteCap}
	}
	phase0AObservers.records += records
	phase0AObservers.bytes += bytes
	return nil
}

func phase0AObserverForRunLocked(core *Core, namespace CoreRunNamespace) (*phase0AObserver, error) {
	if !phase0AObservers.active {
		return nil, &Phase0AError{Kind: Phase0AErrorSessionInactive, Namespace: namespace}
	}
	observer := phase0AObservers.byCore[core]
	if observer == nil || observer.run != namespace {
		return nil, &Phase0AError{Kind: Phase0AErrorStaleNamespace, Namespace: namespace}
	}
	return observer, nil
}

func phase0AObserverForNamespaceLocked(core *Core, namespace CoreRunNamespace) (*phase0AObserver, error) {
	observer, err := phase0AObserverForRunLocked(core, namespace)
	if err != nil {
		return nil, err
	}
	if observer.failure != nil {
		return nil, phase0AExistingFailureLocked(observer)
	}
	if !observer.active {
		return nil, &Phase0AError{Kind: Phase0AErrorStaleNamespace, Namespace: namespace}
	}
	return observer, nil
}

func phase0ACurrentTransaction(observer *phase0AObserver) uint64 {
	if len(observer.frames) == 0 {
		return 0
	}
	return observer.frames[len(observer.frames)-1].TransactionID
}

func phase0AStickyLocked(observer *phase0AObserver, err *Phase0AError) error {
	if observer.failure == nil {
		copy := *err
		observer.failure = &copy
	}
	copy := *observer.failure
	return &copy
}

func phase0AExistingFailureLocked(observer *phase0AObserver) error {
	copy := *observer.failure
	return &copy
}

func Phase0AObserverProof(core *Core, namespace CoreRunNamespace) (Phase0AProofSnapshot, error) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer, err := phase0AObserverForRunLocked(core, namespace)
	if err != nil {
		return Phase0AProofSnapshot{}, err
	}
	if !observer.active && observer.failure == nil {
		return Phase0AProofSnapshot{}, &Phase0AError{Kind: Phase0AErrorStaleNamespace, Namespace: namespace}
	}
	snapshot := Phase0AProofSnapshot{
		Namespace:       namespace,
		OccurrenceCount: observer.occurrenceCount,
		OccurrenceBytes: observer.occurrenceBytes,
	}
	snapshot.Mutations = append(snapshot.Mutations, observer.mutations...)
	snapshot.ReductionOccurrences = append(snapshot.ReductionOccurrences, observer.reductionOccurrences...)
	snapshot.TrailingExtraMigrations = append(snapshot.TrailingExtraMigrations, observer.route.migrations...)
	snapshot.Candidates = append(snapshot.Candidates, observer.factor.candidates...)
	snapshot.Expressions = append(snapshot.Expressions, observer.factor.expressions...)
	snapshot.Bindings = append(snapshot.Bindings, observer.factor.bindings...)
	snapshot.Transitions = append(snapshot.Transitions, observer.factor.transitions...)
	snapshot.Selectors = append(snapshot.Selectors, observer.factor.selectors...)
	snapshot.SelectorRoutes = append(snapshot.SelectorRoutes, observer.factor.selectorRoutes...)
	snapshot.PopRoutes = append(snapshot.PopRoutes, observer.route.popRoutes...)
	snapshot.PopRouteLinks = append(snapshot.PopRouteLinks, observer.route.popLinks...)
	snapshot.SelectionCapabilities = append(snapshot.SelectionCapabilities, observer.route.capabilities...)
	snapshot.AcceptedSelections = append(snapshot.AcceptedSelections, observer.route.acceptedSelections...)
	snapshot.AcceptedLinks = append(snapshot.AcceptedLinks, observer.route.acceptedLinks...)
	snapshot.SelectedOccurrences = append(snapshot.SelectedOccurrences, observer.route.selectedOccurrences...)
	snapshot.SelectedOccurrenceTrees = append(snapshot.SelectedOccurrenceTrees, observer.route.selectedTrees...)
	snapshot.Frames = append(snapshot.Frames, observer.frames...)
	if observer.firstPoison != nil {
		copy := *observer.firstPoison
		snapshot.FirstPoison = &copy
	}
	if observer.failure != nil {
		copy := *observer.failure
		snapshot.Failure = &copy
		return snapshot, snapshot.Failure
	}
	return snapshot, nil
}

func phase0AObserveMark(core *Core, transaction, parent uint64) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer := phase0AObservers.byCore[core]
	if observer == nil || !observer.active || observer.failure != nil {
		return
	}
	if transaction == 0 || (len(observer.frames) == 0 && parent != 0) || (len(observer.frames) != 0 && observer.frames[len(observer.frames)-1].TransactionID != parent) {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: observer.run, Detail: "mark parent mismatch"})
		return
	}
	limit := phase0AObservers.limits.MaxFrames
	if limit == 0 {
		limit = math.MaxUint64
	}
	if uint64(len(observer.frames)) >= limit {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorFrameCap, Namespace: observer.run})
		return
	}
	if err := phase0AChargeManyLocked(3, phase0AFrameRecordBytes+phase0ASidecarFrameBytes+phase0ARouteFrameBytes); err != nil {
		phase0AStickyLocked(observer, err.(*Phase0AError))
		return
	}
	observer.frames = append(observer.frames, Phase0ATransactionFrame{TransactionID: transaction, ParentID: parent, MutationStart: uint64(len(observer.mutations))})
	phase0AFactorMarkLocked(observer)
	phase0ARouteMarkLocked(observer)
}

func phase0AObserveRollback(core *Core, transaction uint64, cause Phase0ARollbackCause) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer := phase0AObservers.byCore[core]
	if observer == nil || !observer.active {
		return
	}
	if observer.failure != nil {
		if len(observer.frames) != 0 && observer.frames[len(observer.frames)-1].TransactionID == transaction {
			frame := observer.frames[len(observer.frames)-1]
			for index := frame.MutationStart; index < uint64(len(observer.mutations)); index++ {
				observer.mutations[index].RolledBack = true
				observer.mutations[index].RollbackTransaction = transaction
				observer.mutations[index].RollbackCause = cause
			}
			phase0ATombstoneReductionOccurrencesLocked(observer, transaction, cause)
			phase0AFactorRollbackLocked(observer, transaction, cause)
			phase0ARouteRollbackLocked(observer, transaction, cause)
			if observer.reduction.active && observer.reduction.transaction == transaction {
				observer.reduction = phase0AReductionConstruction{}
			}
			observer.frames = observer.frames[:len(observer.frames)-1]
		}
		return
	}
	if len(observer.frames) == 0 || observer.frames[len(observer.frames)-1].TransactionID != transaction {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: observer.run, Detail: "rollback transaction mismatch"})
		return
	}
	frame := observer.frames[len(observer.frames)-1]
	for index := frame.MutationStart; index < uint64(len(observer.mutations)); index++ {
		observer.mutations[index].RolledBack = true
		observer.mutations[index].RollbackTransaction = transaction
		observer.mutations[index].RollbackCause = cause
	}
	phase0ATombstoneReductionOccurrencesLocked(observer, transaction, cause)
	phase0AFactorRollbackLocked(observer, transaction, cause)
	phase0ARouteRollbackLocked(observer, transaction, cause)
	if observer.reduction.active && observer.reduction.transaction == transaction {
		observer.reduction = phase0AReductionConstruction{}
	}
	observer.frames = observer.frames[:len(observer.frames)-1]
}

func phase0ATombstoneReductionOccurrencesLocked(observer *phase0AObserver, transaction uint64, cause Phase0ARollbackCause) {
	for index := range observer.reductionOccurrences {
		record := &observer.reductionOccurrences[index]
		mutationIndex, ok := observer.occurrenceIndex[record.Occurrence]
		if record.RolledBack || !ok || mutationIndex >= uint64(len(observer.mutations)) || !observer.mutations[mutationIndex].RolledBack {
			continue
		}
		record.RolledBack = true
		record.RollbackTransaction = transaction
		record.RollbackCause = cause
	}
}

func phase0AObserveCommit(core *Core, transaction uint64) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer := phase0AObservers.byCore[core]
	if observer == nil || !observer.active {
		return
	}
	if observer.failure != nil {
		if len(observer.frames) != 0 && observer.frames[len(observer.frames)-1].TransactionID == transaction {
			if observer.reduction.active && observer.reduction.transaction == transaction {
				observer.reduction = phase0AReductionConstruction{}
			}
			phase0AFactorCommitLocked(observer)
			phase0ARouteCommitLocked(observer)
			observer.frames = observer.frames[:len(observer.frames)-1]
		}
		return
	}
	if len(observer.frames) == 0 || observer.frames[len(observer.frames)-1].TransactionID != transaction {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: observer.run, Detail: "commit transaction mismatch"})
		return
	}
	if observer.reduction.active && observer.reduction.transaction == transaction {
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: observer.run, Detail: "commit with active reduction construction"})
		observer.reduction = phase0AReductionConstruction{}
	}
	for _, candidate := range observer.factor.candidates {
		if candidate.TransactionID == transaction && !candidate.RolledBack && !candidate.Resolved {
			phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorUnsupportedProof, Namespace: observer.run, Detail: "commit with unresolved construction candidate"})
			break
		}
	}
	phase0AFactorCommitLocked(observer)
	phase0ARouteCommitLocked(observer)
	observer.frames = observer.frames[:len(observer.frames)-1]
}

func phase0AObserveFirstPoison(core *Core, transaction uint64, kind Phase0APoisonKind) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer := phase0AObservers.byCore[core]
	if observer == nil || !observer.active || observer.firstPoison != nil {
		return
	}
	observer.firstPoison = &Phase0APoisonRecord{TransactionID: transaction, Kind: kind}
}

func phase0ASetRollbackCause(core *Core, cause Phase0ARollbackCause) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	if observer := phase0AObservers.byCore[core]; observer != nil && observer.active {
		observer.pendingRollback = cause
	}
}

func phase0ATakeRollbackCause(core *Core) Phase0ARollbackCause {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer := phase0AObservers.byCore[core]
	if observer == nil || !observer.active {
		return Phase0ARollbackUnknown
	}
	cause := observer.pendingRollback
	observer.pendingRollback = Phase0ARollbackUnknown
	return cause
}

func phase0AObserveSchedulerPoison(core *Core, token SchedulerTransactionToken, kind Phase0APoisonKind) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer := phase0AObservers.byCore[core]
	if observer == nil || !observer.active || !core.schedulerFrame.active || core.schedulerFrame.poisoned == nil {
		return
	}
	transaction := core.schedulerFrame.mark.transaction
	if observer.reduction.active && observer.reduction.transaction == transaction {
		observer.reduction = phase0AReductionConstruction{}
	}
	if observer.firstPoison == nil {
		if token.owner != core {
			kind = Phase0APoisonWrongCore
		} else if token.epoch == 0 || token.epoch != core.schedulerFrame.epoch || token.transaction != transaction {
			kind = Phase0APoisonStaleToken
		} else if len(core.transactions) == 0 || core.transactions[len(core.transactions)-1] != token.transaction {
			kind = Phase0APoisonNonTopToken
		}
		observer.firstPoison = &Phase0APoisonRecord{TransactionID: transaction, Kind: kind}
	}
}
