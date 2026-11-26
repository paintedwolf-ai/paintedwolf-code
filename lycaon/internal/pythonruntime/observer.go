package pythonruntime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// ErrStreamSealed is returned by SealedWriter once its StopLatch has tripped
// or its output budget is exhausted.
var (
	ErrStreamSealed        = errors.New("guest output stream sealed after terminal stop")
	ErrOutputLimitExceeded = errors.New("guest output byte limit exceeded")
)

// EffectFamily classifies a reached host operation for admission and receipts.
type EffectFamily string

const (
	EffectFilesystemRead     EffectFamily = "filesystem_read"
	EffectFilesystemWrite    EffectFamily = "filesystem_write"
	EffectFilesystemMetadata EffectFamily = "filesystem_metadata"
	EffectImport             EffectFamily = "import"
	EffectHTTP               EffectFamily = "http"
	EffectSubprocess         EffectFamily = "subprocess"
	EffectPreviewListener    EffectFamily = "preview_listener"
	EffectHostWork           EffectFamily = "host_work"
)

// FrameConfidence distinguishes interpreter-declared source locations from
// host-confirmed bindings and unknown attribution.
type FrameConfidence string

const (
	FrameDeclared      FrameConfidence = "declared"
	FrameHostConfirmed FrameConfidence = "host_confirmed"
	FrameUnknown       FrameConfidence = "unknown"
)

// DeclaredFrame carries best-available source attribution for a reached effect.
type DeclaredFrame struct {
	File       string          `json:"file,omitempty"`
	Line       int             `json:"line,omitempty"`
	Function   string          `json:"function,omitempty"`
	Confidence FrameConfidence `json:"confidence"`
}

// ReachedOperation represents a single host operation reached by the guest.
type ReachedOperation struct {
	OperationID   string          `json:"operation_id"`
	ExecutionID   string          `json:"execution_id"`
	Family        EffectFamily    `json:"family"`
	Operation     string          `json:"operation"`
	Target        CanonicalTarget `json:"target"`
	Args          map[string]any  `json:"args,omitempty"`
	DeclaredFrame *DeclaredFrame  `json:"declared_frame,omitempty"`
	HostWorkCost  uint64          `json:"host_work_cost,omitempty"`
}

// DecisionKind is the host admission outcome for a ReachedOperation.
type DecisionKind string

const (
	DecisionAllow DecisionKind = "allow"
	DecisionDeny  DecisionKind = "deny"
	DecisionHold  DecisionKind = "hold"
	DecisionStop  DecisionKind = "stop"
)

// StopCode identifies a typed terminal host stop.
type StopCode string

const (
	StopEffectNotAllowed    StopCode = "COMMAND_EFFECT_NOT_ALLOWED"
	StopRevisionConflict    StopCode = "COMMAND_REVISION_CONFLICT"
	StopWorkLimitExceeded   StopCode = "PYTHON_WORK_LIMIT_EXCEEDED"
	StopMemoryLimitExceeded StopCode = "PYTHON_MEMORY_LIMIT_EXCEEDED"
	StopOutputLimitExceeded StopCode = "PYTHON_OUTPUT_LIMIT_EXCEEDED"
	StopCancelled           StopCode = "PYTHON_CANCELLED"
)

// TerminalStop records the latched host stop that terminated guest execution.
type TerminalStop struct {
	Code        StopCode  `json:"code"`
	Reason      string    `json:"reason,omitempty"`
	OperationID string    `json:"operation_id,omitempty"`
	LatchedAt   time.Time `json:"latched_at"`
}

// OperationDecision is returned by OperationObserver.Admit.
type OperationDecision struct {
	Kind            DecisionKind  `json:"kind"`
	Errno           Errno         `json:"errno,omitempty"`
	Stop            *TerminalStop `json:"stop,omitempty"`
	ApprovalLeaseID string        `json:"approval_lease_id,omitempty"`
	Reason          string        `json:"reason,omitempty"`
}

// OperationOutcome records the observed result of an admitted host operation.
type OperationOutcome struct {
	Succeeded        bool   `json:"succeeded"`
	Errno            Errno  `json:"errno,omitempty"`
	BytesTransferred int64  `json:"bytes_transferred,omitempty"`
	HostWorkCharged  uint64 `json:"host_work_charged,omitempty"`
	Error            string `json:"error,omitempty"`
}

// OperationObserver mediates reached-operation admission and outcome recording
// (implemented by PY04).
type OperationObserver interface {
	Admit(ctx context.Context, op ReachedOperation) (OperationDecision, error)
	RecordOutcome(ctx context.Context, opID string, outcome OperationOutcome)
}

// StopLatch atomically latches the first terminal host stop, seals all
// registered guest output writers, and cancels guest execution.
type StopLatch struct {
	stopped atomic.Bool
	mu      sync.Mutex
	stop    *TerminalStop
	cancel  context.CancelFunc
	sealers []func()
	now     func() time.Time
}

// NewStopLatch creates a StopLatch that invokes cancel when triggered.
func NewStopLatch(cancel context.CancelFunc) *StopLatch {
	return &StopLatch{
		cancel: cancel,
		now:    time.Now,
	}
}

// RegisterSealer registers a callback invoked synchronously when the latch
// trips. If the latch has already tripped, fn is invoked immediately.
func (l *StopLatch) RegisterSealer(fn func()) {
	if fn == nil {
		return
	}
	l.mu.Lock()
	if l.stopped.Load() {
		l.mu.Unlock()
		fn()
		return
	}
	l.sealers = append(l.sealers, fn)
	l.mu.Unlock()
}

// Trigger latches stop if no terminal stop has been recorded yet, seals all
// registered output streams, and cancels guest execution. Returns true only
// for the first trigger.
func (l *StopLatch) Trigger(stop TerminalStop) bool {
	if !l.stopped.CompareAndSwap(false, true) {
		return false
	}
	l.mu.Lock()
	if stop.LatchedAt.IsZero() && l.now != nil {
		stop.LatchedAt = l.now().UTC()
	}
	copied := stop
	l.stop = &copied
	sealers := append([]func(){}, l.sealers...)
	cancel := l.cancel
	l.mu.Unlock()

	for _, seal := range sealers {
		seal()
	}
	if cancel != nil {
		cancel()
	}
	return true
}

// Stopped reports whether a terminal stop has been latched.
func (l *StopLatch) Stopped() bool {
	return l.stopped.Load()
}

// Stop returns a copy of the latched TerminalStop, or nil if not stopped.
func (l *StopLatch) Stop() *TerminalStop {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stop == nil {
		return nil
	}
	copied := *l.stop
	return &copied
}

// SealedWriter wraps an io.Writer so that once its StopLatch trips or its byte
// budget is exhausted, zero post-stop bytes are ever delivered downstream.
type SealedWriter struct {
	dst       io.Writer
	latch     *StopLatch
	maxBytes  int64
	mu        sync.Mutex
	sealed    atomic.Bool
	hadError  atomic.Bool
	written   int64
	dropped   int64
	truncated bool
}

// NewSealedWriter constructs a SealedWriter bound to latch and an optional
// maxBytes cap (<= 0 means unbounded).
func NewSealedWriter(dst io.Writer, latch *StopLatch, maxBytes int64) *SealedWriter {
	w := &SealedWriter{
		dst:      dst,
		latch:    latch,
		maxBytes: maxBytes,
	}
	if latch != nil {
		latch.RegisterSealer(w.Seal)
	}
	return w
}

// Seal atomically blocks all subsequent writes from reaching dst.
func (w *SealedWriter) Seal() {
	w.mu.Lock()
	w.sealed.Store(true)
	w.mu.Unlock()
}

// Sealed reports whether the writer has been sealed.
func (w *SealedWriter) Sealed() bool {
	return w.sealed.Load()
}

// WrittenBytes returns the number of bytes delivered downstream before sealing.
func (w *SealedWriter) WrittenBytes() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.written
}

// DroppedBytes returns the number of bytes rejected after sealing or cap.
func (w *SealedWriter) DroppedBytes() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dropped
}

// HadError reports whether any write failed, was truncated, or was rejected
// after sealing.
func (w *SealedWriter) HadError() bool {
	return w.hadError.Load()
}

// Write delivers bytes to dst unless sealed or over budget.
func (w *SealedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.sealed.Load() || (w.latch != nil && w.latch.Stopped()) {
		w.sealed.Store(true)
		w.hadError.Store(true)
		w.dropped += int64(len(p))
		w.mu.Unlock()
		return 0, ErrStreamSealed
	}
	if w.maxBytes > 0 && w.written+int64(len(p)) > w.maxBytes {
		remaining := w.maxBytes - w.written
		writtenNow := 0
		if remaining > 0 && w.dst != nil {
			n, err := w.dst.Write(p[:remaining])
			writtenNow = n
			w.written += int64(n)
			if err != nil {
				w.sealed.Store(true)
				w.hadError.Store(true)
				w.mu.Unlock()
				return writtenNow, err
			}
		}
		w.dropped += int64(len(p) - writtenNow)
		w.truncated = true
		w.sealed.Store(true)
		w.hadError.Store(true)
		latch := w.latch
		w.mu.Unlock()
		if latch != nil {
			latch.Trigger(TerminalStop{
				Code:   StopOutputLimitExceeded,
				Reason: ErrOutputLimitExceeded.Error(),
			})
		}
		return writtenNow, ErrOutputLimitExceeded
	}
	var n int
	var err error
	if w.dst != nil {
		n, err = w.dst.Write(p)
	} else {
		n = len(p)
	}
	if err != nil {
		w.hadError.Store(true)
	}
	w.written += int64(n)
	w.mu.Unlock()
	return n, err
}
