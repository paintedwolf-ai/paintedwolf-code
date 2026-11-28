package pythonadmission

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

const (
	// ExternalProcessObservationLimits documents the explicit observation
	// boundary for external subprocess execution.
	ExternalProcessObservationLimits = "external_process_boundary: child_syscalls_and_internal_imports_unobserved"
)

var (
	// ErrEmptySubprocessArgv is returned when a subprocess request has no argv[0].
	ErrEmptySubprocessArgv = errors.New("subprocess request requires non-empty argv")
	// ErrUnsupportedSharedView is returned when a subprocess needs unpublished
	// overlay bytes without a mediated view or explicit stream.
	ErrUnsupportedSharedView = errors.New("subprocess requires unpublished overlay bytes without mediated view or explicit stream")
	// ErrSubprocessDenied is returned when OperationObserver refuses a subprocess launch.
	ErrSubprocessDenied = errors.New("subprocess launch refused by admission policy")
)

// SubprocessRequest describes an argv-preserving subprocess launch crossing
// from Portable Python into the host process boundary.
type SubprocessRequest struct {
	OperationID           string
	Argv                  []string
	CWD                   string
	Env                   map[string]string
	NeedsUnpublishedBytes bool
	HasMediatedView       bool
	HasExplicitStream     bool
	DeclaredFrame         *pythonruntime.DeclaredFrame
}

// SubprocessResult captures the outcome of an admitted subprocess execution.
type SubprocessResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Record   SubprocessRecord
}

// SubprocessRunner executes an already-admitted argv-preserving subprocess
// under the native process boundary.
type SubprocessRunner interface {
	Run(ctx context.Context, req SubprocessRequest) (exitCode int, stdout, stderr []byte, err error)
}

// SubprocessAdmitterConfig configures a SubprocessAdmitter.
type SubprocessAdmitterConfig struct {
	ExecutionID string
	CallID      string
	Observer    pythonruntime.OperationObserver
	Runner      SubprocessRunner
	Clock       pythonruntime.Clock
	Evidence    EvidenceSink
}

// SubprocessAdmitter enforces shared-view preconditions, admits argv-preserving
// subprocess launches through OperationObserver(EffectSubprocess), and records
// external-process observation limits.
type SubprocessAdmitter struct {
	cfg      SubprocessAdmitterConfig
	clock    pythonruntime.Clock
	evidence EvidenceSink
	seq      atomic.Uint64
}

// NewSubprocessAdmitter constructs a SubprocessAdmitter.
func NewSubprocessAdmitter(cfg SubprocessAdmitterConfig) *SubprocessAdmitter {
	clk := cfg.Clock
	if clk == nil {
		clk = pythonruntime.NewSystemClock()
	}
	ev := cfg.Evidence
	if ev == nil {
		ev = NewMemoryEvidenceStore()
	}
	return &SubprocessAdmitter{
		cfg:      cfg,
		clock:    clk,
		evidence: ev,
	}
}

// Execute validates shared-view compatibility, admits the exact argv through
// OperationObserver, invokes Runner, and records a SubprocessRecord.
func (a *SubprocessAdmitter) Execute(ctx context.Context, req SubprocessRequest) (*SubprocessResult, error) {
	if len(req.Argv) == 0 || req.Argv[0] == "" {
		return nil, ErrEmptySubprocessArgv
	}
	opID := req.OperationID
	if opID == "" {
		opID = fmt.Sprintf("subproc-%s-%d", a.cfg.ExecutionID, a.seq.Add(1))
	}

	// Refuse unsupported shared-view combinations BEFORE launch.
	if req.NeedsUnpublishedBytes && !req.HasMediatedView && !req.HasExplicitStream {
		rec := SubprocessRecord{
			ExecutionID:       a.cfg.ExecutionID,
			OperationID:       opID,
			CallID:            a.cfg.CallID,
			Argv:              append([]string(nil), req.Argv...),
			CWD:               req.CWD,
			MediatedView:      false,
			ObservationLimits: ExternalProcessObservationLimits,
			Decision:          pythonruntime.DecisionStop,
			StopCode:          pythonruntime.StopEffectNotAllowed,
			Reason:            ErrUnsupportedSharedView.Error(),
			RecordedAt:        a.clock.Now().UTC(),
		}
		a.evidence.RecordSubprocess(rec)
		return &SubprocessResult{Record: rec}, ErrUnsupportedSharedView
	}

	if a.cfg.Observer != nil {
		dec, err := a.cfg.Observer.Admit(ctx, pythonruntime.ReachedOperation{
			OperationID: opID,
			ExecutionID: a.cfg.ExecutionID,
			Family:      pythonruntime.EffectSubprocess,
			Operation:   "subprocess.exec",
			Target: pythonruntime.CanonicalTarget{
				Namespace: pythonruntime.Namespace("subprocess"),
				RelPath:   req.Argv[0],
			},
			Args: map[string]any{
				"argv":          append([]string(nil), req.Argv...),
				"cwd":           req.CWD,
				"mediated_view": req.HasMediatedView,
			},
			DeclaredFrame: req.DeclaredFrame,
		})
		if err != nil {
			return nil, err
		}
		if dec.Kind != pythonruntime.DecisionAllow {
			var stopCode pythonruntime.StopCode
			if dec.Stop != nil {
				stopCode = dec.Stop.Code
			}
			rec := SubprocessRecord{
				ExecutionID:       a.cfg.ExecutionID,
				OperationID:       opID,
				CallID:            a.cfg.CallID,
				Argv:              append([]string(nil), req.Argv...),
				CWD:               req.CWD,
				MediatedView:      req.HasMediatedView,
				ObservationLimits: ExternalProcessObservationLimits,
				Decision:          dec.Kind,
				StopCode:          stopCode,
				Reason:            dec.Reason,
				RecordedAt:        a.clock.Now().UTC(),
			}
			a.evidence.RecordSubprocess(rec)
			return &SubprocessResult{Record: rec}, fmt.Errorf("%w: %s", ErrSubprocessDenied, dec.Reason)
		}
	}

	if a.cfg.Runner == nil {
		return nil, errors.New("subprocess runner not configured")
	}

	exitCode, stdout, stderr, runErr := a.cfg.Runner.Run(ctx, req)
	rec := SubprocessRecord{
		ExecutionID:       a.cfg.ExecutionID,
		OperationID:       opID,
		CallID:            a.cfg.CallID,
		Argv:              append([]string(nil), req.Argv...),
		CWD:               req.CWD,
		MediatedView:      req.HasMediatedView,
		ObservationLimits: ExternalProcessObservationLimits,
		Decision:          pythonruntime.DecisionAllow,
		ExitCode:          exitCode,
		RecordedAt:        a.clock.Now().UTC(),
	}
	if runErr != nil {
		rec.Reason = runErr.Error()
	}
	a.evidence.RecordSubprocess(rec)
	if a.cfg.Observer != nil {
		outcome := pythonruntime.OperationOutcome{
			Succeeded:        runErr == nil && exitCode == 0,
			BytesTransferred: int64(len(stdout) + len(stderr)),
		}
		if runErr != nil {
			outcome.Error = runErr.Error()
		}
		a.cfg.Observer.RecordOutcome(ctx, opID, outcome)
	}

	return &SubprocessResult{
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
		Record:   rec,
	}, runErr
}
