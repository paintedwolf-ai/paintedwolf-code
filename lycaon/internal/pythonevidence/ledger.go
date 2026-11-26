package pythonevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
)

var (
	// ErrMissingParentOperation is returned when a child event lacks explicit
	// parent operation ancestry.
	ErrMissingParentOperation = errors.New("child evidence record missing parent_operation_id")
	// ErrMissingReadWitness is returned when a document read record claims
	// delivered guest bytes without an actual successful guest read witness.
	ErrMissingReadWitness = errors.New("bytes_delivered_to_guest requires at least one successful guest read witness")
	// ErrInvalidAttributionKind is returned when a Python workspace change is
	// attributed to a non-Python mechanism (such as shell_redirect).
	ErrInvalidAttributionKind = errors.New("workspace publication attribution must be python_workspace_publication")
)

// AttributionKind identifies the host mechanism that produced a workspace change.
type AttributionKind string

const (
	// AttributionPythonWorkspacePublication is the sole valid attribution for
	// Portable Python workspace publications.
	AttributionPythonWorkspacePublication AttributionKind = "python_workspace_publication"
	// AttributionShellRedirect is a negative-control value from prototype shell
	// redirection and MUST NEVER be accepted for Python workspace mutations.
	AttributionShellRedirect AttributionKind = "shell_redirect"
)

// ValidatePythonAttribution verifies that kind is python_workspace_publication
// and explicitly rejects shell_redirect or other non-Python labels.
func ValidatePythonAttribution(kind AttributionKind) error {
	if kind != AttributionPythonWorkspacePublication {
		return fmt.Errorf("%w: got %q", ErrInvalidAttributionKind, kind)
	}
	return nil
}

// SourceUnit records an imported, submitted, or dynamically generated source
// unit associated with an execution.
type SourceUnit struct {
	UnitID           string                        `json:"unit_id"`
	Kind             string                        `json:"kind"` // "submitted", "imported", "dynamic"
	DeclaredName     string                        `json:"declared_name,omitempty"`
	CanonicalTarget  pythonruntime.CanonicalTarget `json:"canonical_target,omitempty"`
	ContentSHA256    string                        `json:"content_sha256,omitempty"`
	CapturedRevision string                        `json:"captured_revision,omitempty"`
	LineCount        int                           `json:"line_count,omitempty"`
}

// SourceBindingProof supplies an independent host-established binding between
// an executed code unit digest, a captured source unit, and a valid line range.
// A matching filename alone NEVER establishes a SourceBindingProof.
type SourceBindingProof struct {
	CodeUnitDigest       string `json:"code_unit_digest"`
	CapturedSourceUnitID string `json:"captured_source_unit_id"`
	CapturedSourceSHA256 string `json:"captured_source_sha256"`
	StartLine            int    `json:"start_line"`
	EndLine              int    `json:"end_line"`
	HostVerified         bool   `json:"host_verified"`
}

// AttributedFrame carries source location attribution alongside its strict
// host confidence classification.
type AttributedFrame struct {
	File                 string                        `json:"file,omitempty"`
	Line                 int                           `json:"line,omitempty"`
	Function             string                        `json:"function,omitempty"`
	Confidence           pythonruntime.FrameConfidence `json:"confidence"`
	CodeUnitDigest       string                        `json:"code_unit_digest,omitempty"`
	CapturedSourceUnitID string                        `json:"captured_source_unit_id,omitempty"`
}

// AttributeFrame classifies an interpreter-reported frame. Interpreter frames
// remain "declared" (or "unknown") unless proof independently binds the
// executed code unit digest, captured source unit digest, and line range.
func AttributeFrame(declared *pythonruntime.DeclaredFrame, proof *SourceBindingProof) AttributedFrame {
	if declared == nil || (declared.File == "" && declared.Function == "" && declared.Line == 0) {
		return AttributedFrame{Confidence: pythonruntime.FrameUnknown}
	}
	out := AttributedFrame{
		File:       declared.File,
		Line:       declared.Line,
		Function:   declared.Function,
		Confidence: pythonruntime.FrameDeclared,
	}
	if proof != nil &&
		proof.HostVerified &&
		proof.CodeUnitDigest != "" &&
		proof.CapturedSourceUnitID != "" &&
		proof.CapturedSourceSHA256 != "" &&
		proof.CodeUnitDigest == proof.CapturedSourceSHA256 &&
		proof.StartLine > 0 &&
		proof.EndLine >= proof.StartLine &&
		declared.Line >= proof.StartLine &&
		declared.Line <= proof.EndLine {
		out.Confidence = pythonruntime.FrameHostConfirmed
		out.CodeUnitDigest = proof.CodeUnitDigest
		out.CapturedSourceUnitID = proof.CapturedSourceUnitID
	}
	return out
}

// WriteAttribution carries originating write/open frame, flush/close frame,
// and final host publication identity separately so a late flush never
// fabricates an earlier write site.
type WriteAttribution struct {
	WriteOriginFrame  AttributedFrame `json:"write_origin_frame"`
	FlushCloseFrame   AttributedFrame `json:"flush_close_frame"`
	HostPublicationID string          `json:"host_publication_id,omitempty"`
	AttributionKind   AttributionKind `json:"attribution_kind,omitempty"`
}

// ExecutionRecord pins the identity, actor, view, surface, submitted source,
// imported units, and runtime image for one Portable Python execution.
type ExecutionRecord struct {
	ExecutionID           string                      `json:"execution_id"`
	CallID                string                      `json:"call_id"`
	Actor                 string                      `json:"actor"`
	View                  string                      `json:"view"`
	Surface               string                      `json:"surface"`
	SubmittedSource       string                      `json:"submitted_source,omitempty"`
	SubmittedSourceSHA256 string                      `json:"submitted_source_sha256,omitempty"`
	ImportedUnits         []SourceUnit                `json:"imported_units,omitempty"`
	RuntimeImageID        string                      `json:"runtime_image_id"`
	StartedAt             time.Time                   `json:"started_at"`
	CompletedAt           time.Time                   `json:"completed_at,omitempty"`
	CleanCompletion       bool                        `json:"clean_completion"`
	ExitCode              int                         `json:"exit_code"`
	Stop                  *pythonruntime.TerminalStop `json:"stop,omitempty"`
}

// Five strictly separate evidence facts:
// 1. DocumentOpenedFact
// 2. BytesDeliveredFact
// 3. DerivedOutputFact
// 4. ScreenedModelOutputFact
// 5. NativeSourceRangeFact

// DocumentOpenedFact records that the guest opened a document handle. Opening a
// document NEVER establishes bytes delivered to the guest or model grounding.
type DocumentOpenedFact struct {
	ExecutionID       string                         `json:"execution_id"`
	OperationID       string                         `json:"operation_id"`
	ParentOperationID string                         `json:"parent_operation_id"`
	Target            pythonruntime.CanonicalTarget  `json:"target"`
	Revision          pythondocuments.DocumentRevision `json:"revision"`
	UnsavedDraft      bool                           `json:"unsaved_draft,omitempty"`
	RecordedAt        time.Time                      `json:"recorded_at"`
}

// BytesDeliveredFact records actual bytes delivered to the guest, witnessed by
// successful guest read operations (never snapshot length on open).
type BytesDeliveredFact struct {
	ExecutionID           string                         `json:"execution_id"`
	OperationID           string                         `json:"operation_id"`
	ParentOperationID     string                         `json:"parent_operation_id"`
	Target                pythonruntime.CanonicalTarget  `json:"target"`
	Revision              pythondocuments.DocumentRevision `json:"revision"`
	BytesDeliveredToGuest int64                          `json:"bytes_delivered_to_guest"`
	ReadWitnessCount      int                            `json:"read_witness_count"`
	RecordedAt            time.Time                      `json:"recorded_at"`
}

// DerivedOutputFact records secret-screened stdout/stderr emitted by the guest.
type DerivedOutputFact struct {
	ExecutionID   string    `json:"execution_id"`
	Stream        string    `json:"stream"` // "stdout" | "stderr"
	ScreenedText  string    `json:"screened_text"`
	ByteLength    int64     `json:"byte_length"`
	RedactedCount int       `json:"redacted_count,omitempty"`
	ArtifactID    string    `json:"artifact_id,omitempty"`
	RecordedAt    time.Time `json:"recorded_at"`
}

// ScreenedModelOutputFact records screened program output delivered to the
// model. It NEVER mints a model source citation or a verification verdict.
type ScreenedModelOutputFact struct {
	ExecutionID              string    `json:"execution_id"`
	ScreenedText             string    `json:"screened_text"`
	RedactedCount            int       `json:"redacted_count,omitempty"`
	MintsSourceCitation      bool      `json:"mints_source_citation"`
	MintsVerificationVerdict bool      `json:"mints_verification_verdict"`
	RecordedAt               time.Time `json:"recorded_at"`
}

// NativeSourceRangeFact records a bounded, revision-bound native source range
// delivered to the model with an authoritative host handle. This is the sole
// fact that can ground a model source citation or finding.
type NativeSourceRangeFact struct {
	ExecutionID string                           `json:"execution_id,omitempty"`
	Target      pythonruntime.CanonicalTarget    `json:"target"`
	Revision    pythondocuments.DocumentRevision `json:"revision"`
	StartLine   int                              `json:"start_line"`
	EndLine     int                              `json:"end_line"`
	HostHandle  string                           `json:"host_handle"`
	DeliveredAt time.Time                        `json:"delivered_at"`
}

// LedgerAdmission links a reached AdmissionRecord with ancestry and frame attribution.
type LedgerAdmission struct {
	ParentOperationID string                          `json:"parent_operation_id,omitempty"`
	Record            pythonadmission.AdmissionRecord `json:"record"`
	Frame             AttributedFrame                 `json:"frame"`
	RedactedCount     int                             `json:"redacted_count,omitempty"`
}

// LedgerDocumentRead links a DocumentReadRecord with parent ancestry and witness count.
type LedgerDocumentRead struct {
	ParentOperationID string                             `json:"parent_operation_id"`
	Record            pythondocuments.DocumentReadRecord `json:"record"`
	ReadWitnessCount  int                                `json:"read_witness_count"`
	WriteOrigin       AttributedFrame                    `json:"write_origin"`
	FlushClose        AttributedFrame                    `json:"flush_close"`
}

// LedgerOverlayEvent links an OverlayEventRecord with parent ancestry and separate frames.
type LedgerOverlayEvent struct {
	ParentOperationID string                             `json:"parent_operation_id"`
	Record            pythondocuments.OverlayEventRecord `json:"record"`
	WriteOrigin       AttributedFrame                    `json:"write_origin"`
	FlushClose        AttributedFrame                    `json:"flush_close"`
}

// LedgerPublication links a PublicationRecord with parent ancestry and host attribution.
type LedgerPublication struct {
	ParentOperationID string                            `json:"parent_operation_id"`
	Record            pythondocuments.PublicationRecord `json:"record"`
	Attribution       WriteAttribution                  `json:"attribution"`
}

// LedgerHTTP links a screened HTTPRecord with parent ancestry.
type LedgerHTTP struct {
	ParentOperationID string                     `json:"parent_operation_id"`
	Record            pythonadmission.HTTPRecord `json:"record"`
	RedactedCount     int                        `json:"redacted_count,omitempty"`
}

// LedgerPreviewListener links a PreviewListenerRecord with parent ancestry.
type LedgerPreviewListener struct {
	ParentOperationID string                                `json:"parent_operation_id"`
	Record            pythonadmission.PreviewListenerRecord `json:"record"`
}

// LedgerSubprocess links a screened SubprocessRecord with parent ancestry and
// explicit opaque/uncertain outcome classification.
type LedgerSubprocess struct {
	ParentOperationID string                           `json:"parent_operation_id"`
	Record            pythonadmission.SubprocessRecord `json:"record"`
	RedactedCount     int                              `json:"redacted_count,omitempty"`
	UncertainOutcome  bool                             `json:"uncertain_outcome"`
}

// ExecutionLedger is the thread-safe durable evidence accumulator for one
// Portable Python execution.
type ExecutionLedger struct {
	mu       sync.Mutex
	exec     ExecutionRecord
	screener SecretScreener
	now      func() time.Time

	admissions       []LedgerAdmission
	reads            []LedgerDocumentRead
	overlayEvents    []LedgerOverlayEvent
	publications     []LedgerPublication
	httpRecords      []LedgerHTTP
	previewListeners []LedgerPreviewListener
	subprocesses     []LedgerSubprocess

	openedDocuments      []DocumentOpenedFact
	deliveredBytes       []BytesDeliveredFact
	derivedOutputs       []DerivedOutputFact
	screenedModelOutputs []ScreenedModelOutputFact
	nativeSourceRanges   []NativeSourceRangeFact
}

// NewExecutionLedger constructs an ExecutionLedger, screening SubmittedSource
// and computing its SHA-256 digest from the raw source before persistence.
func NewExecutionLedger(exec ExecutionRecord, screener SecretScreener, now func() time.Time) *ExecutionLedger {
	if now == nil {
		now = time.Now
	}
	scr := effectiveScreener(screener)
	if exec.SubmittedSource != "" && exec.SubmittedSourceSHA256 == "" {
		sum := sha256.Sum256([]byte(exec.SubmittedSource))
		exec.SubmittedSourceSHA256 = hex.EncodeToString(sum[:])
	}
	exec.SubmittedSource, _ = scr.ScreenText(exec.SubmittedSource)
	if exec.StartedAt.IsZero() {
		exec.StartedAt = now().UTC()
	}
	return &ExecutionLedger{
		exec:     exec,
		screener: scr,
		now:      now,
	}
}

// RecordCompletion finalizes the execution outcome on the ledger.
func (l *ExecutionLedger) RecordCompletion(clean bool, exitCode int, stop *pythonruntime.TerminalStop) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.exec.CleanCompletion = clean
	l.exec.ExitCode = exitCode
	if stop != nil {
		copied := *stop
		copied.Reason, _ = l.screener.ScreenText(copied.Reason)
		l.exec.Stop = &copied
	}
	l.exec.CompletedAt = l.now().UTC()
}

// AddImportedUnit records an imported or dynamic source unit on the execution.
func (l *ExecutionLedger) AddImportedUnit(unit SourceUnit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.exec.ImportedUnits = append(l.exec.ImportedUnits, unit)
}

// RecordAdmission records a reached syscall admission with strict frame
// attribution and secret screening.
func (l *ExecutionLedger) RecordAdmission(rec pythonadmission.AdmissionRecord, parentOpID string, proof *SourceBindingProof) {
	screened, n := ScreenAdmissionRecord(l.screener, rec)
	frame := AttributeFrame(screened.DeclaredFrame, proof)
	l.mu.Lock()
	l.admissions = append(l.admissions, LedgerAdmission{
		ParentOperationID: parentOpID,
		Record:            screened,
		Frame:             frame,
		RedactedCount:     n,
	})
	l.mu.Unlock()
}

// RecordDocumentRead records a document read event, enforcing parent ancestry
// and the strict delivered-byte rule: BytesDeliveredToGuest > 0 requires
// readWitnessCount > 0 (snapshot length on open is never a delivery witness).
func (l *ExecutionLedger) RecordDocumentRead(
	rec pythondocuments.DocumentReadRecord,
	parentOpID string,
	readWitnessCount int,
	writeProof, flushProof *SourceBindingProof,
) error {
	if strings.TrimSpace(parentOpID) == "" {
		return ErrMissingParentOperation
	}
	if rec.BytesDeliveredToGuest < 0 {
		return errors.New("bytes_delivered_to_guest cannot be negative")
	}
	if rec.BytesDeliveredToGuest > 0 && readWitnessCount <= 0 {
		return ErrMissingReadWitness
	}
	ts := rec.RecordedAt
	if ts.IsZero() {
		ts = l.now().UTC()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reads = append(l.reads, LedgerDocumentRead{
		ParentOperationID: parentOpID,
		Record:            rec,
		ReadWitnessCount:  readWitnessCount,
		WriteOrigin:       AttributeFrame(rec.WriteOriginFrame, writeProof),
		FlushClose:        AttributeFrame(rec.FlushCloseFrame, flushProof),
	})
	l.openedDocuments = append(l.openedDocuments, DocumentOpenedFact{
		ExecutionID:       rec.ExecutionID,
		OperationID:       rec.OperationID,
		ParentOperationID: parentOpID,
		Target:            rec.Target,
		Revision:          rec.Revision,
		UnsavedDraft:      rec.UnsavedDraft,
		RecordedAt:        ts,
	})
	l.deliveredBytes = append(l.deliveredBytes, BytesDeliveredFact{
		ExecutionID:           rec.ExecutionID,
		OperationID:           rec.OperationID,
		ParentOperationID:     parentOpID,
		Target:                rec.Target,
		Revision:              rec.Revision,
		BytesDeliveredToGuest: rec.BytesDeliveredToGuest,
		ReadWitnessCount:      readWitnessCount,
		RecordedAt:            ts,
	})
	return nil
}

// RecordOverlayEvent records a per-program overlay mutation with parent ancestry.
func (l *ExecutionLedger) RecordOverlayEvent(
	rec pythondocuments.OverlayEventRecord,
	parentOpID string,
	writeProof, flushProof *SourceBindingProof,
) error {
	if strings.TrimSpace(parentOpID) == "" {
		return ErrMissingParentOperation
	}
	l.mu.Lock()
	l.overlayEvents = append(l.overlayEvents, LedgerOverlayEvent{
		ParentOperationID: parentOpID,
		Record:            rec,
		WriteOrigin:       AttributeFrame(rec.WriteOriginFrame, writeProof),
		FlushClose:        AttributeFrame(rec.FlushCloseFrame, flushProof),
	})
	l.mu.Unlock()
	return nil
}

// RecordPublication records a host settlement publication or revision conflict,
// enforcing parent ancestry and python_workspace_publication attribution.
func (l *ExecutionLedger) RecordPublication(
	rec pythondocuments.PublicationRecord,
	parentOpID string,
	hostPublicationID string,
	kind AttributionKind,
	writeProof, flushProof *SourceBindingProof,
) error {
	if strings.TrimSpace(parentOpID) == "" {
		return ErrMissingParentOperation
	}
	if err := ValidatePythonAttribution(kind); err != nil {
		return err
	}
	l.mu.Lock()
	l.publications = append(l.publications, LedgerPublication{
		ParentOperationID: parentOpID,
		Record:            rec,
		Attribution: WriteAttribution{
			WriteOriginFrame:  AttributeFrame(rec.WriteOriginFrame, writeProof),
			FlushCloseFrame:   AttributeFrame(rec.FlushCloseFrame, flushProof),
			HostPublicationID: hostPublicationID,
			AttributionKind:   kind,
		},
	})
	l.mu.Unlock()
	return nil
}

// RecordHTTP screens and persists a host-mediated HTTP record with parent ancestry.
func (l *ExecutionLedger) RecordHTTP(rec pythonadmission.HTTPRecord, parentOpID string) error {
	if strings.TrimSpace(parentOpID) == "" {
		return ErrMissingParentOperation
	}
	screened, n := ScreenHTTPRecord(l.screener, rec)
	l.mu.Lock()
	l.httpRecords = append(l.httpRecords, LedgerHTTP{
		ParentOperationID: parentOpID,
		Record:            screened,
		RedactedCount:     n,
	})
	l.mu.Unlock()
	return nil
}

// RecordPreviewListener persists a supervised preview listener record with parent ancestry.
func (l *ExecutionLedger) RecordPreviewListener(rec pythonadmission.PreviewListenerRecord, parentOpID string) error {
	if strings.TrimSpace(parentOpID) == "" {
		return ErrMissingParentOperation
	}
	l.mu.Lock()
	l.previewListeners = append(l.previewListeners, LedgerPreviewListener{
		ParentOperationID: parentOpID,
		Record:            rec,
	})
	l.mu.Unlock()
	return nil
}

// RecordSubprocess screens and persists a subprocess boundary record with parent ancestry.
func (l *ExecutionLedger) RecordSubprocess(rec pythonadmission.SubprocessRecord, parentOpID string) error {
	if strings.TrimSpace(parentOpID) == "" {
		return ErrMissingParentOperation
	}
	screened, n := ScreenSubprocessRecord(l.screener, rec)
	uncertain := !screened.MediatedView || strings.TrimSpace(screened.ObservationLimits) != ""
	l.mu.Lock()
	l.subprocesses = append(l.subprocesses, LedgerSubprocess{
		ParentOperationID: parentOpID,
		Record:            screened,
		RedactedCount:     n,
		UncertainOutcome:  uncertain,
	})
	l.mu.Unlock()
	return nil
}

// RecordDerivedOutput screens guest stdout/stderr before persisting fact #3.
func (l *ExecutionLedger) RecordDerivedOutput(stream, rawText, artifactID string) DerivedOutputFact {
	screened, n := l.screener.ScreenText(rawText)
	fact := DerivedOutputFact{
		ExecutionID:   l.exec.ExecutionID,
		Stream:        stream,
		ScreenedText:  screened,
		ByteLength:    int64(len(screened)),
		RedactedCount: n,
		ArtifactID:    artifactID,
		RecordedAt:    l.now().UTC(),
	}
	l.mu.Lock()
	l.derivedOutputs = append(l.derivedOutputs, fact)
	l.mu.Unlock()
	return fact
}

// RecordScreenedModelOutput screens output delivered to the model (fact #4).
// Program output NEVER mints a model source citation or verification verdict.
func (l *ExecutionLedger) RecordScreenedModelOutput(rawText string) ScreenedModelOutputFact {
	screened, n := l.screener.ScreenText(rawText)
	fact := ScreenedModelOutputFact{
		ExecutionID:              l.exec.ExecutionID,
		ScreenedText:             screened,
		RedactedCount:            n,
		MintsSourceCitation:      false,
		MintsVerificationVerdict: false,
		RecordedAt:               l.now().UTC(),
	}
	l.mu.Lock()
	l.screenedModelOutputs = append(l.screenedModelOutputs, fact)
	l.mu.Unlock()
	return fact
}

// RecordNativeSourceRange records a revision-bound native source range delivered
// to the model (fact #5). Only this fact can ground model source citations.
func (l *ExecutionLedger) RecordNativeSourceRange(fact NativeSourceRangeFact) error {
	if strings.TrimSpace(fact.HostHandle) == "" {
		return errors.New("native source range requires host_handle")
	}
	if !fact.Revision.HasIdentity() {
		return errors.New("native source range requires revision identity")
	}
	if fact.StartLine <= 0 || fact.EndLine < fact.StartLine {
		return errors.New("native source range requires valid 1-based line range")
	}
	if fact.DeliveredAt.IsZero() {
		fact.DeliveredAt = l.now().UTC()
	}
	if fact.ExecutionID == "" {
		fact.ExecutionID = l.exec.ExecutionID
	}
	l.mu.Lock()
	l.nativeSourceRanges = append(l.nativeSourceRanges, fact)
	l.mu.Unlock()
	return nil
}

// Snapshot getters returning copies of ledger state.

func (l *ExecutionLedger) Execution() ExecutionRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := l.exec
	cp.ImportedUnits = append([]SourceUnit(nil), l.exec.ImportedUnits...)
	return cp
}

func (l *ExecutionLedger) Admissions() []LedgerAdmission {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LedgerAdmission(nil), l.admissions...)
}

func (l *ExecutionLedger) DocumentReads() []LedgerDocumentRead {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LedgerDocumentRead(nil), l.reads...)
}

func (l *ExecutionLedger) OverlayEvents() []LedgerOverlayEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LedgerOverlayEvent(nil), l.overlayEvents...)
}

func (l *ExecutionLedger) Publications() []LedgerPublication {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LedgerPublication(nil), l.publications...)
}

func (l *ExecutionLedger) HTTPRecords() []LedgerHTTP {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LedgerHTTP(nil), l.httpRecords...)
}

func (l *ExecutionLedger) PreviewListeners() []LedgerPreviewListener {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LedgerPreviewListener(nil), l.previewListeners...)
}

func (l *ExecutionLedger) Subprocesses() []LedgerSubprocess {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LedgerSubprocess(nil), l.subprocesses...)
}

func (l *ExecutionLedger) OpenedDocuments() []DocumentOpenedFact {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]DocumentOpenedFact(nil), l.openedDocuments...)
}

func (l *ExecutionLedger) DeliveredBytes() []BytesDeliveredFact {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]BytesDeliveredFact(nil), l.deliveredBytes...)
}

func (l *ExecutionLedger) DerivedOutputs() []DerivedOutputFact {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]DerivedOutputFact(nil), l.derivedOutputs...)
}

func (l *ExecutionLedger) ScreenedModelOutputs() []ScreenedModelOutputFact {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]ScreenedModelOutputFact(nil), l.screenedModelOutputs...)
}

// ModelSourceCitations returns only revision-bound native source ranges
// delivered to the model. Guest reads and screened program outputs never mint
// citations.
func (l *ExecutionLedger) ModelSourceCitations() []NativeSourceRangeFact {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]NativeSourceRangeFact(nil), l.nativeSourceRanges...)
}
