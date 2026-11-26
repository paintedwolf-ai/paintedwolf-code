package pythondocuments

import (
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

// DocumentReadRecord captures structured read-side evidence for PY05,
// distinguishing the captured host revision and bytes delivered to the guest
// from any derived output or model-delivered source ranges.
type DocumentReadRecord struct {
	ExecutionID           string                        `json:"execution_id"`
	OperationID           string                        `json:"operation_id"`
	Target                pythonruntime.CanonicalTarget `json:"target"`
	Revision              DocumentRevision              `json:"revision"`
	BytesDeliveredToGuest int64                         `json:"bytes_delivered_to_guest"`
	UnsavedDraft          bool                          `json:"unsaved_draft"`
	WriteOriginFrame      *pythonruntime.DeclaredFrame  `json:"write_origin_frame,omitempty"`
	FlushCloseFrame       *pythonruntime.DeclaredFrame  `json:"flush_close_frame,omitempty"`
	RecordedAt            time.Time                     `json:"recorded_at"`
}

// OverlayEventRecord captures a single per-program overlay mutation event
// separately from final host publication.
type OverlayEventRecord struct {
	ExecutionID      string                         `json:"execution_id"`
	OperationID      string                         `json:"operation_id"`
	Sequence         uint64                         `json:"sequence"`
	Operation        string                         `json:"operation"`
	Target           pythonruntime.CanonicalTarget  `json:"target"`
	PreviousTarget   *pythonruntime.CanonicalTarget `json:"previous_target,omitempty"`
	BytesAffected    int64                          `json:"bytes_affected,omitempty"`
	WriteOriginFrame *pythonruntime.DeclaredFrame   `json:"write_origin_frame,omitempty"`
	FlushCloseFrame  *pythonruntime.DeclaredFrame   `json:"flush_close_frame,omitempty"`
	RecordedAt       time.Time                      `json:"recorded_at"`
}

// PublicationRecord captures a single host settlement publication attempt (or
// revision conflict) for PY05 receipts and attribution.
type PublicationRecord struct {
	ExecutionID       string                          `json:"execution_id"`
	OperationID       string                          `json:"operation_id"`
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
	RecordOverlayEvent(rec OverlayEventRecord)
	RecordPublication(rec PublicationRecord)
}

// EvidenceLog is a thread-safe in-memory EvidenceSink for tests and callers
// that inspect emitted PY05 records after execution.
type EvidenceLog struct {
	mu           sync.Mutex
	reads        []DocumentReadRecord
	overlay      []OverlayEventRecord
	publications []PublicationRecord
}

// NewEvidenceLog constructs an empty EvidenceLog.
func NewEvidenceLog() *EvidenceLog {
	return &EvidenceLog{}
}

func (l *EvidenceLog) RecordDocumentRead(rec DocumentReadRecord) {
	l.mu.Lock()
	l.reads = append(l.reads, rec)
	l.mu.Unlock()
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

// Reads returns a copy of recorded DocumentReadRecord entries.
func (l *EvidenceLog) Reads() []DocumentReadRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]DocumentReadRecord, len(l.reads))
	copy(out, l.reads)
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
