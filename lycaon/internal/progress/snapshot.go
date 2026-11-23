package progress

import (
	"slices"

	api "github.com/lycaon/lycaon/pkg/api"
)

// UnaccountedResult identifies a settled worker round awaiting acknowledgement.
type UnaccountedResult struct {
	Handle  string `json:"handle"`
	Worker  string `json:"worker,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// Reconciliation identifies settled worker finishes awaiting acknowledgement.
type Reconciliation struct {
	Jobs        []string            `json:"-"`
	Settled     []string            `json:"settled_work"`
	Unaccounted []UnaccountedResult `json:"unaccounted_results"`
}

// Snapshot binds ordered progress rows to their workflow run and durable version.
type Snapshot struct {
	Rows            []Row           `json:"rows"`
	RunID           string          `json:"run_id,omitempty"`
	Reconciliation  *Reconciliation `json:"reconciliation,omitempty"`
	version         string
	documentVersion uint64
	nextItemSeq     uint64
}

// NewSnapshot constructs an in-memory snapshot with cloned rows.
func NewSnapshot(runID string, rows ...Row) Snapshot {
	cloned := slices.Clone(rows)
	if cloned == nil {
		cloned = []Row{}
	}
	return Snapshot{Rows: cloned, RunID: runID, nextItemSeq: uint64(len(cloned) + 1)}
}

// DocumentVersion returns the committed monotonic document version.
func (s Snapshot) DocumentVersion() uint64 { return s.documentVersion }

// WithDocumentVersion returns a copy with the committed document version attached.
func (s Snapshot) WithDocumentVersion(version uint64) Snapshot {
	s.documentVersion = version
	return s
}

// NextItemSeq returns the next unallocated item sequence number (>= 1).
func (s Snapshot) NextItemSeq() uint64 {
	if s.nextItemSeq < 1 {
		return 1
	}
	return s.nextItemSeq
}

// Missing reports whether the snapshot has zero rows.
func (s Snapshot) Missing() bool { return len(s.Rows) == 0 }

// OpenStepCount returns remaining pending rows.
func (s Snapshot) OpenStepCount() int { return OpenStepCount(s.Rows) }

// HasOpenSteps reports whether any pending row remains.
func (s Snapshot) HasOpenSteps() bool { return HasOpenSteps(s.Rows) }

// AllTerminal reports whether at least one row exists and none are pending.
func (s Snapshot) AllTerminal() bool { return AllTerminal(s.Rows) }

// CloseCounts returns completed, pending, and not-pursued counts.
func (s Snapshot) CloseCounts() (done, pending, na int) { return CloseCounts(s.Rows) }

// Summary tallies the snapshot's rows.
func (s Snapshot) Summary() Summary { return Summarize(s.Rows) }

// VisibleSteps returns display-clamped non-optional steps.
func (s Snapshot) VisibleSteps() []api.ProgressStep { return VisibleSteps(s.Rows) }

// Derive projects up to limit non-optional steps.
func (s Snapshot) Derive(limit int) ProgressResult { return DeriveProgress(s.Rows, limit) }

// RenderMarkdown formats the snapshot rows as checklist lines.
func (s Snapshot) RenderMarkdown() string { return RenderMarkdown(s.Rows) }

// Equal reports whether two snapshots hold identical rows, run binding, version, and reconciliation.
func (s Snapshot) Equal(other Snapshot) bool {
	return slices.Equal(s.Rows, other.Rows) &&
		s.RunID == other.RunID &&
		s.version == other.version &&
		s.documentVersion == other.documentVersion &&
		s.NextItemSeq() == other.NextItemSeq() &&
		reconciliationEqual(s.Reconciliation, other.Reconciliation)
}

func reconciliationEqual(a, b *Reconciliation) bool {
	if a == nil || b == nil {
		return a == b
	}
	return slices.Equal(a.Jobs, b.Jobs) &&
		slices.Equal(a.Settled, b.Settled) &&
		slices.Equal(a.Unaccounted, b.Unaccounted)
}

func snapshotFromDB(runID string, rows []Row, updatedAt string, docVersion, nextSeq uint64) Snapshot {
	if rows == nil {
		rows = []Row{}
	}
	if nextSeq < 1 {
		nextSeq = 1
	}
	return Snapshot{
		Rows:            rows,
		RunID:           runID,
		version:         updatedAt,
		documentVersion: docVersion,
		nextItemSeq:     nextSeq,
	}
}

// Transitions distinguish deliverable state changes from non-state edits.
type Transitions struct {
	Changed       bool
	ClosedItemIDs []string
}

// ClassifyTransitions compares rows sharing the same ID across snapshots.
func ClassifyTransitions(before, after Snapshot) Transitions {
	if len(before.Rows) == 0 || len(after.Rows) == 0 {
		return Transitions{}
	}
	prevByID := make(map[string]Row, len(before.Rows))
	for _, r := range before.Rows {
		if r.ID != "" {
			prevByID[r.ID] = r
		}
	}
	var out Transitions
	for _, next := range after.Rows {
		prev, ok := prevByID[next.ID]
		if !ok || prev.State == next.State {
			continue
		}
		out.Changed = true
		if prev.State == ProgressStatePending && (next.State == ProgressStateDone || next.State == ProgressStateNA) {
			out.ClosedItemIDs = append(out.ClosedItemIDs, next.ID)
		}
	}
	return out
}
