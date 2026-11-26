package pythondocuments

import (
	"context"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

type parentOpIDContextKey struct{}

// WithParentOperationID binds the originating reached-operation ID from the
// single admission owner (PY02's WorkspaceSysFS) onto ctx so child document
// reads, overlay events, and settlement publications link back to their
// originating syscall admission record.
func WithParentOperationID(ctx context.Context, opID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, parentOpIDContextKey{}, opID)
}

// ParentOperationID extracts the originating reached-operation ID from ctx,
// or returns the empty string if none is bound.
func ParentOperationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(parentOpIDContextKey{}).(string)
	return v
}

// ByteRange represents a half-open byte interval [Start, End) actually
// delivered to the guest via GuestFile.Read.
type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// DocumentReadRecord captures structured read-side evidence for PY05,
// distinguishing the revision witness recorded at Open from the actual bytes
// and ranges delivered to the guest through GuestFile.Read.
type DocumentReadRecord struct {
	ExecutionID           string                        `json:"execution_id"`
	OperationID           string                        `json:"operation_id"`
	ParentOperationID     string                        `json:"parent_operation_id,omitempty"`
	Target                pythonruntime.CanonicalTarget `json:"target"`
	Revision              DocumentRevision              `json:"revision"`
	BytesDeliveredToGuest int64                         `json:"bytes_delivered_to_guest"`
	DeliveredRanges       []ByteRange                   `json:"delivered_ranges,omitempty"`
	UnsavedDraft          bool                          `json:"unsaved_draft"`
	WriteOriginFrame      *pythonruntime.DeclaredFrame  `json:"write_origin_frame,omitempty"`
	FlushCloseFrame       *pythonruntime.DeclaredFrame  `json:"flush_close_frame,omitempty"`
	RecordedAt            time.Time                     `json:"recorded_at"`
}

// OverlayEventRecord captures a single per-program overlay mutation event
// separately from final host publication.
type OverlayEventRecord struct {
	ExecutionID       string                         `json:"execution_id"`
	OperationID       string                         `json:"operation_id"`
	ParentOperationID string                         `json:"parent_operation_id,omitempty"`
	Sequence          uint64                         `json:"sequence"`
	Operation         string                         `json:"operation"`
	Target            pythonruntime.CanonicalTarget  `json:"target"`
	PreviousTarget    *pythonruntime.CanonicalTarget `json:"previous_target,omitempty"`
	BytesAffected     int64                          `json:"bytes_affected,omitempty"`
	WriteOriginFrame  *pythonruntime.DeclaredFrame   `json:"write_origin_frame,omitempty"`
	FlushCloseFrame   *pythonruntime.DeclaredFrame   `json:"flush_close_frame,omitempty"`
	RecordedAt        time.Time                      `json:"recorded_at"`
}

// PublicationRecord captures a single host settlement publication attempt (or
// revision conflict) for PY05 receipts and attribution.
type PublicationRecord struct {
	ExecutionID       string                          `json:"execution_id"`
	OperationID       string                          `json:"operation_id"`
	ParentOperationID string                          `json:"parent_operation_id,omitempty"`
	Target            pythonruntime.CanonicalTarget   `json:"target"`
	Operation         string                          `json:"operation"`
	PreviousTarget    *pythonruntime.CanonicalTarget  `json:"previous_target,omitempty"`
	BaseRevision      DocumentRevision                `json:"base_revision"`
	CommittedRevision *DocumentRevision               `json:"committed_revision,omitempty"`
	BytesWritten      int64                           `json:"bytes_written,omitempty"`
	Conflict          *pythonruntime.RevisionConflict `json:"conflict,omitempty"`
	PartialFailure    bool                            `json:"partial_failure,omitempty"`
	WriteOriginFrame  *pythonruntime.DeclaredFrame    `json:"write_origin_frame,omitempty"`
	FlushCloseFrame   *pythonruntime.DeclaredFrame    `json:"flush_close_frame,omitempty"`
	RecordedAt        time.Time                       `json:"recorded_at"`
}

// EvidenceSink receives structured document read, overlay, and publication
// records emitted by OverlayAdapter (consumed by PY05).
type EvidenceSink interface {
	RecordDocumentRead(rec DocumentReadRecord)
	RecordDocumentReadBytes(operationID string, rng ByteRange)
	RecordOverlayEvent(rec OverlayEventRecord)
	RecordPublication(rec PublicationRecord)
}

// EvidenceLog is a thread-safe in-memory EvidenceSink for tests and callers
// that inspect emitted PY05 records after execution.
type EvidenceLog struct {
	mu           sync.Mutex
	reads        []DocumentReadRecord
	readIndex    map[string]int
	overlay      []OverlayEventRecord
	publications []PublicationRecord
}

// NewEvidenceLog constructs an empty EvidenceLog.
func NewEvidenceLog() *EvidenceLog {
	return &EvidenceLog{
		readIndex: make(map[string]int),
	}
}

func (l *EvidenceLog) RecordDocumentRead(rec DocumentReadRecord) {
	l.mu.Lock()
	l.readIndex[rec.OperationID] = len(l.reads)
	l.reads = append(l.reads, rec)
	l.mu.Unlock()
}

func (l *EvidenceLog) RecordDocumentReadBytes(operationID string, rng ByteRange) {
	if rng.End <= rng.Start {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	idx, ok := l.readIndex[operationID]
	if !ok {
		return
	}
	rec := &l.reads[idx]
	rec.BytesDeliveredToGuest += rng.End - rng.Start
	if n := len(rec.DeliveredRanges); n > 0 && rec.DeliveredRanges[n-1].End == rng.Start {
		rec.DeliveredRanges[n-1].End = rng.End
	} else {
		rec.DeliveredRanges = append(rec.DeliveredRanges, rng)
	}
}

func (l *EvidenceLog) RecordOverlayEvent(rec OverlayEventRecord) {
	l.mu.Lock()
	l.overlay = append(l.overlay, rec)
	l.mu.Unlock()
}

func (l *EvidenceLog) RecordPublication(rec PublicationRecord) {
	l.mu.Lock()
	l.publications = append(l.publications, rec)
	l.mu.Unlock()
}

// Reads returns a deep copy of recorded DocumentReadRecord entries.
func (l *EvidenceLog) Reads() []DocumentReadRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]DocumentReadRecord, len(l.reads))
	for i, r := range l.reads {
		if len(r.DeliveredRanges) > 0 {
			r.DeliveredRanges = append([]ByteRange(nil), r.DeliveredRanges...)
		}
		out[i] = r
	}
	return out
}

// OverlayEvents returns a copy of recorded OverlayEventRecord entries.
func (l *EvidenceLog) OverlayEvents() []OverlayEventRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]OverlayEventRecord, len(l.overlay))
	copy(out, l.overlay)
	return out
}

// Publications returns a copy of recorded PublicationRecord entries.
func (l *EvidenceLog) Publications() []PublicationRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]PublicationRecord, len(l.publications))
	copy(out, l.publications)
	return out
}
