package bgprocess

import (
	"context"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/indexwatch"
)

// JobMode describes how a command enters the process registry.
type JobMode string

const (
	JobModeAwaited    JobMode = "awaited"
	JobModeBackground JobMode = "background"
)

// TerminationReason is the host-observed reason a command reached terminal state.
type TerminationReason string

const (
	TerminationExited          TerminationReason = "exited"
	TerminationTimedOut        TerminationReason = "timed_out"
	TerminationStoppedByAgent  TerminationReason = "stopped_by_agent"
	TerminationStoppedByUser   TerminationReason = "stopped_by_user"
	TerminationChatDeleted     TerminationReason = "chat_deleted"
	TerminationProjectDeleted  TerminationReason = "project_deleted"
	TerminationRootDetached    TerminationReason = "root_detached"
	TerminationAppQuit         TerminationReason = "app_quit"
	TerminationWorkerCompleted TerminationReason = "worker_completed"
	TerminationWorkerCancelled TerminationReason = "worker_cancelled"
)

// IsStopped reports whether termination was requested rather than reached by process exit or timeout.
func (r TerminationReason) IsStopped() bool {
	switch r {
	case TerminationStoppedByAgent,
		TerminationStoppedByUser,
		TerminationChatDeleted,
		TerminationProjectDeleted,
		TerminationRootDetached,
		TerminationAppQuit,
		TerminationWorkerCompleted,
		TerminationWorkerCancelled:
		return true
	default:
		return false
	}
}

// DeliversToSession reports whether a terminal command should publish session-scoped completion state.
func (r TerminationReason) DeliversToSession() bool {
	switch r {
	case TerminationChatDeleted,
		TerminationProjectDeleted,
		TerminationAppQuit,
		TerminationWorkerCompleted,
		TerminationWorkerCancelled:
		return false
	default:
		return true
	}
}

type terminationReasonKey struct{}

// WithTerminationReason records the outer teardown reason on ctx when none is already set.
func WithTerminationReason(ctx context.Context, reason TerminationReason) context.Context {
	if ctx == nil || reason == "" {
		return ctx
	}
	if existing, ok := ctx.Value(terminationReasonKey{}).(TerminationReason); ok && existing != "" {
		return ctx
	}
	return context.WithValue(ctx, terminationReasonKey{}, reason)
}

// TerminationReasonFromContext returns the teardown reason on ctx, or fallback when absent.
func TerminationReasonFromContext(ctx context.Context, fallback TerminationReason) TerminationReason {
	if ctx != nil {
		if reason, ok := ctx.Value(terminationReasonKey{}).(TerminationReason); ok && reason != "" {
			return reason
		}
	}
	return fallback
}

var (
	ErrCommandInFlight  = errors.New("awaited command already in flight")
	ErrDuplicateRunning = errors.New("identical command already running")
)

// Hooks are the registry's only outward lifecycle ports.
type Hooks struct {
	Publish  StreamPublisher
	Complete CompletionPublisher
	// Refused hears refusals the kernel made to a running visible job.
	Refused RefusalPublisher
}

// CompletionPublisher receives one terminal result for every visible command job.
type CompletionPublisher func(ctx context.Context, completion Completion)

// PipelineSpec is the complete, immutable identity and lifecycle policy for a command job.
type PipelineSpec struct {
	IsCheck          bool
	SourceRevision   string
	SourceRootDigest string
	Cwd              string
	SessionID        string
	RootSessionID    string
	ProjectID        string
	Request          hostcmd.Request
	Runner           *hostcmd.Runner
	Mode             JobMode
	OriginTool       string
	ToolCallID       string
	RunID            string
	Timeout          time.Duration
	AllowConcurrent  bool
	Facts            confine.SpawnFacts
}

// JobSnapshot is the bounded command ledger exposed to coordinator assembly.
type JobSnapshot struct {
	Handle     string
	Mode       JobMode
	OriginTool string
	StartedAt  time.Time
	Timeout    time.Duration
	Stages     []hostcmd.StageResult
}

// RootProcess identifies one live background process running under a workspace root.
type RootProcess struct {
	Handle    string
	SessionID string
	Cwd       string
}

// StartConflict describes the live jobs that prevented a new command from starting.
type StartConflict struct {
	Kind    error
	Handles []string
}

func (e *StartConflict) Error() string {
	if errors.Is(e.Kind, ErrDuplicateRunning) {
		return ErrDuplicateRunning.Error()
	}
	return ErrCommandInFlight.Error()
}

func (e *StartConflict) Unwrap() error { return e.Kind }

// Completion is the terminal, host-authoritative result for one command job.
type Completion struct {
	IsCheck           bool
	SourceRevision    string
	SourceRootDigest  string
	Cwd               string
	Handle            string
	SessionID         string
	ProjectID         string
	OriginTool        string
	ToolCallID        string
	RunID             string
	Mode              JobMode
	StartedAt         time.Time
	FinishedAt        time.Time
	TerminationReason TerminationReason
	ExitCode          int
	// Failure is the run error the exit status does not carry.
	Failure *hostcmd.ExecFailure
	Stages  []hostcmd.StageResult
	Tail    string
	// BoundaryRefusal attributes a failed run to confinement.
	BoundaryRefusal string
	// GuidanceCodes classify a confinement refusal.
	GuidanceCodes []string
	// Observation records the confinement boundary.
	Observation confine.Observation
	// IndexWatch is the Git index captured at spawn; the consumer releases it.
	IndexWatch indexwatch.Snapshot
}

func (c Completion) Elapsed() time.Duration {
	if c.StartedAt.IsZero() || c.FinishedAt.IsZero() {
		return 0
	}
	return c.FinishedAt.Sub(c.StartedAt)
}
