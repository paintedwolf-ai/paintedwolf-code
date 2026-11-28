package pythonadmission

import (
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

// AdmissionRecord captures one reached host operation's pinned identity,
// admission decision, authoritative resolved target, and observed outcome.
// This record shape is consumed by PY05 evidence and ledger publication.
type AdmissionRecord struct {
	ExecutionID         string                          `json:"execution_id"`
	OperationID         string                          `json:"operation_id"`
	CallID              string                          `json:"call_id"`
	Actor               string                          `json:"actor"`
	View                string                          `json:"view"`
	Surface             string                          `json:"surface"`
	Family              pythonruntime.EffectFamily      `json:"family"`
	Operation           string                          `json:"operation"`
	Target              pythonruntime.CanonicalTarget   `json:"target"`
	AuthoritativeTarget pythonruntime.CanonicalTarget   `json:"authoritative_target"`
	Decision            pythonruntime.DecisionKind      `json:"decision"`
	Errno               pythonruntime.Errno             `json:"errno,omitempty"`
	StopCode            pythonruntime.StopCode          `json:"stop_code,omitempty"`
	ApprovalLeaseID     string                          `json:"approval_lease_id,omitempty"`
	DeclaredFrame       *pythonruntime.DeclaredFrame    `json:"declared_frame,omitempty"`
	Outcome             *pythonruntime.OperationOutcome `json:"outcome,omitempty"`
	Reason              string                          `json:"reason,omitempty"`
	RecordedAt          time.Time                       `json:"recorded_at"`
}

// HTTPRedirectHop records one redirect step evaluated at destination admission.
type HTTPRedirectHop struct {
	FromURL    string                 `json:"from_url"`
	ToURL      string                 `json:"to_url"`
	StatusCode int                    `json:"status_code"`
	Admitted   bool                   `json:"admitted"`
	StopCode   pythonruntime.StopCode `json:"stop_code,omitempty"`
	Reason     string                 `json:"reason,omitempty"`
}

// HTTPRecord captures one host-mediated HTTP request executed for requests.Session.
type HTTPRecord struct {
	ExecutionID    string            `json:"execution_id"`
	OperationID    string            `json:"operation_id"`
	CallID         string            `json:"call_id,omitempty"`
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	FinalURL       string            `json:"final_url,omitempty"`
	Redirects      []HTTPRedirectHop `json:"redirects,omitempty"`
	Status         int               `json:"status,omitempty"`
	BytesSent      int64             `json:"bytes_sent,omitempty"`
	BytesReceived  int64             `json:"bytes_received,omitempty"`
	PartialFailure bool              `json:"partial_failure,omitempty"`
	Error          string            `json:"error,omitempty"`
	RecordedAt     time.Time         `json:"recorded_at"`
}

// PreviewListenerRecord captures the lifecycle and identity of a supervised
// static preview listener bound for `python[3] -m http.server`.
type PreviewListenerRecord struct {
	JobID       string    `json:"job_id"`
	ExecutionID string    `json:"execution_id,omitempty"`
	CallID      string    `json:"call_id,omitempty"`
	Address     string    `json:"address"`
	Port        int       `json:"port"`
	Directory   string    `json:"directory"`
	Active      bool      `json:"active"`
	StartedAt   time.Time `json:"started_at"`
	StoppedAt   time.Time `json:"stopped_at,omitempty"`
}

// SubprocessRecord captures one argv-preserving subprocess admission and
// execution boundary record.
type SubprocessRecord struct {
	ExecutionID       string                     `json:"execution_id"`
	OperationID       string                     `json:"operation_id"`
	CallID            string                     `json:"call_id,omitempty"`
	Argv              []string                   `json:"argv"`
	CWD               string                     `json:"cwd,omitempty"`
	MediatedView      bool                       `json:"mediated_view"`
	ObservationLimits string                     `json:"observation_limits"`
	Decision          pythonruntime.DecisionKind `json:"decision"`
	StopCode          pythonruntime.StopCode     `json:"stop_code,omitempty"`
	ExitCode          int                        `json:"exit_code,omitempty"`
	Reason            string                     `json:"reason,omitempty"`
	RecordedAt        time.Time                  `json:"recorded_at"`
}

// EvidenceSink receives structured admission, HTTP, preview listener, and
// subprocess records for durable ledger publication (PY05).
type EvidenceSink interface {
	RecordAdmission(rec AdmissionRecord)
	UpdateAdmissionOutcome(operationID string, outcome pythonruntime.OperationOutcome)
	RecordHTTP(rec HTTPRecord)
	RecordPreviewListener(rec PreviewListenerRecord)
	RecordSubprocess(rec SubprocessRecord)
}

// MemoryEvidenceStore is a thread-safe in-memory EvidenceSink.
type MemoryEvidenceStore struct {
	mu          sync.Mutex
	admissions  []AdmissionRecord
	httpRecords []HTTPRecord
	previews    []PreviewListenerRecord
	subprocs    []SubprocessRecord
}

// NewMemoryEvidenceStore returns an empty MemoryEvidenceStore.
func NewMemoryEvidenceStore() *MemoryEvidenceStore {
	return &MemoryEvidenceStore{}
}

// RecordAdmission appends an AdmissionRecord.
func (s *MemoryEvidenceStore) RecordAdmission(rec AdmissionRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.DeclaredFrame != nil {
		copied := *rec.DeclaredFrame
		rec.DeclaredFrame = &copied
	}
	if rec.Outcome != nil {
		copied := *rec.Outcome
		rec.Outcome = &copied
	}
	s.admissions = append(s.admissions, rec)
}

// UpdateAdmissionOutcome attaches outcome to the most recent AdmissionRecord
// matching operationID.
func (s *MemoryEvidenceStore) UpdateAdmissionOutcome(operationID string, outcome pythonruntime.OperationOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.admissions) - 1; i >= 0; i-- {
		if s.admissions[i].OperationID == operationID {
			copied := outcome
			s.admissions[i].Outcome = &copied
			return
		}
	}
}

// RecordHTTP appends an HTTPRecord.
func (s *MemoryEvidenceStore) RecordHTTP(rec HTTPRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(rec.Redirects) > 0 {
		rec.Redirects = append([]HTTPRedirectHop(nil), rec.Redirects...)
	}
	s.httpRecords = append(s.httpRecords, rec)
}

// RecordPreviewListener appends a PreviewListenerRecord.
func (s *MemoryEvidenceStore) RecordPreviewListener(rec PreviewListenerRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.previews = append(s.previews, rec)
}

// RecordSubprocess appends a SubprocessRecord.
func (s *MemoryEvidenceStore) RecordSubprocess(rec SubprocessRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(rec.Argv) > 0 {
		rec.Argv = append([]string(nil), rec.Argv...)
	}
	s.subprocs = append(s.subprocs, rec)
}

// Admissions returns a snapshot copy of all recorded AdmissionRecords.
func (s *MemoryEvidenceStore) Admissions() []AdmissionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AdmissionRecord, len(s.admissions))
	copy(out, s.admissions)
	return out
}

// HTTPRecords returns a snapshot copy of all recorded HTTPRecords.
func (s *MemoryEvidenceStore) HTTPRecords() []HTTPRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]HTTPRecord, len(s.httpRecords))
	copy(out, s.httpRecords)
	return out
}

// PreviewListeners returns a snapshot copy of all recorded PreviewListenerRecords.
func (s *MemoryEvidenceStore) PreviewListeners() []PreviewListenerRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PreviewListenerRecord, len(s.previews))
	copy(out, s.previews)
	return out
}

// Subprocesses returns a snapshot copy of all recorded SubprocessRecords.
func (s *MemoryEvidenceStore) Subprocesses() []SubprocessRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SubprocessRecord, len(s.subprocs))
	copy(out, s.subprocs)
	return out
}
