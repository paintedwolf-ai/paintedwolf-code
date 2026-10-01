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
	TerminationExited   TerminationReason = "exited"
	TerminationStopped  TerminationReason = "stopped"
	TerminationTimedOut TerminationReason = "timed_out"
)

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
