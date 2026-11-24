package secretmatch

import (
	"context"
	"log/slog"
	"sync"
)

// ScreenPhaseRecord captures value-free counts, durations, and opaque IDs for
// one model-screen or credential-evidence step.
type ScreenPhaseRecord struct {
	CallID              string
	ProjectID           string
	BuildID             string
	RootID              string
	Phase               string
	Mode                string
	DurationMS          int64
	RootsChecked        int
	BuildsJoined        int
	BuildIDs            []string
	DirectoriesListed   int
	PathsClassified     int
	CredentialFilesRead int
	ManagedValues       int
	Hits                int
	Decision            string
	Failed              bool
}

type phaseObserverRegistry struct {
	mu      sync.RWMutex
	nextID  uint64
	entries map[uint64]func(ScreenPhaseRecord)
}

var phaseObservers = phaseObserverRegistry{
	entries: make(map[uint64]func(ScreenPhaseRecord)),
}

// RegisterPhaseObserver subscribes fn to value-free screen and evidence phase records.
func RegisterPhaseObserver(fn func(ScreenPhaseRecord)) func() {
	if fn == nil {
		return func() {}
	}
	phaseObservers.mu.Lock()
	phaseObservers.nextID++
	id := phaseObservers.nextID
	phaseObservers.entries[id] = fn
	phaseObservers.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			phaseObservers.mu.Lock()
			delete(phaseObservers.entries, id)
			phaseObservers.mu.Unlock()
		})
	}
}

// EmitPhaseRecord logs a value-free debug record and notifies registered observers.
func EmitPhaseRecord(ctx context.Context, rec ScreenPhaseRecord) {
	slog.DebugContext(ctx, "secret screen phase",
		"call_id", rec.CallID,
		"project_id", rec.ProjectID,
		"build_id", rec.BuildID,
		"root_id", rec.RootID,
		"phase", rec.Phase,
		"mode", rec.Mode,
		"duration_ms", rec.DurationMS,
		"roots_checked", rec.RootsChecked,
		"builds_joined", rec.BuildsJoined,
		"directories_listed", rec.DirectoriesListed,
		"paths_classified", rec.PathsClassified,
		"credential_files_read", rec.CredentialFilesRead,
		"managed_values", rec.ManagedValues,
		"hits", rec.Hits,
		"decision", rec.Decision,
		"failed", rec.Failed,
	)
	phaseObservers.mu.RLock()
	listeners := make([]func(ScreenPhaseRecord), 0, len(phaseObservers.entries))
	for _, fn := range phaseObservers.entries {
		listeners = append(listeners, fn)
	}
	phaseObservers.mu.RUnlock()
	for _, fn := range listeners {
		fn(rec)
	}
}

type screenCallIDKey struct{}

// WithScreenCallID binds an opaque host call or attempt ID for downstream evidence tracing.
func WithScreenCallID(ctx context.Context, callID string) context.Context {
	if callID == "" {
		return ctx
	}
	return context.WithValue(ctx, screenCallIDKey{}, callID)
}

// ScreenCallIDFrom extracts the bound host call ID, if any.
func ScreenCallIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(screenCallIDKey{}).(string)
	return v
}

// EvidenceTrace accumulates value-free counts across one evidence reconciliation call.
type EvidenceTrace struct {
	mu                  sync.Mutex
	ReadinessWaitMS     int64
	RootsChecked        int
	BuildsJoined        int
	BuildIDs            []string
	DirectoriesListed   int
	PathsClassified     int
	CredentialFilesRead int
	ManagedLookupMS     int64
	ManagedValues       int
}

// NoteRootWait records one root readiness check and any joined build.
func (t *EvidenceTrace) NoteRootWait(waitMS int64, buildID string, dirs, classified, filesRead int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.RootsChecked++
	t.ReadinessWaitMS += waitMS
	if buildID != "" {
		t.BuildsJoined++
		t.BuildIDs = append(t.BuildIDs, buildID)
		t.DirectoriesListed += dirs
		t.PathsClassified += classified
		t.CredentialFilesRead += filesRead
	}
}

// Snapshot returns a value copy of the accumulated trace.
func (t *EvidenceTrace) Snapshot() EvidenceTrace {
	if t == nil {
		return EvidenceTrace{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return EvidenceTrace{
		ReadinessWaitMS:     t.ReadinessWaitMS,
		RootsChecked:        t.RootsChecked,
		BuildsJoined:        t.BuildsJoined,
		BuildIDs:            append([]string(nil), t.BuildIDs...),
		DirectoriesListed:   t.DirectoriesListed,
		PathsClassified:     t.PathsClassified,
		CredentialFilesRead: t.CredentialFilesRead,
		ManagedLookupMS:     t.ManagedLookupMS,
		ManagedValues:       t.ManagedValues,
	}
}

type evidenceTraceKey struct{}

// WithEvidenceTrace attaches a fresh EvidenceTrace if none is already bound.
func WithEvidenceTrace(ctx context.Context) (context.Context, *EvidenceTrace) {
	if existing := EvidenceTraceFrom(ctx); existing != nil {
		return ctx, existing
	}
	tr := &EvidenceTrace{}
	return context.WithValue(ctx, evidenceTraceKey{}, tr), tr
}

// EvidenceTraceFrom returns the bound EvidenceTrace, or nil.
func EvidenceTraceFrom(ctx context.Context) *EvidenceTrace {
	tr, _ := ctx.Value(evidenceTraceKey{}).(*EvidenceTrace)
	return tr
}
