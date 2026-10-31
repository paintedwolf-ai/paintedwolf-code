package bgprocess

import (
	"context"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/indexwatch"
)

const (
	processKindPipeline = ""
	processKindPTY      = "pty"
)

type JobLiveness string

const (
	JobLivenessActive JobLiveness = "active"
	// Unknown liveness retains the warning until an exit is observed.
	JobLivenessUnknown JobLiveness = "unknown"
)

// Process is one running or completed background pipeline or pty handle.
type Process struct {
	Handle        string
	SessionID     string
	ProjectID     string
	RootSessionID string
	Stages        []hostcmd.StageResult
	kind          string
	mode          JobMode
	running       bool
	// silent jobs count only against the awaited concurrency ceiling.
	silent bool

	processRuntimeState
	processIOState
	processConfinementState
}

type processRuntimeState struct {
	stopped           bool
	exitCode          int
	hasExit           bool
	startedAt         time.Time
	finishedAt        time.Time
	timeout           time.Duration
	reason            TerminationReason
	terminalPublished bool
	discarded         bool
	done              chan struct{}
	cancel            context.CancelFunc
	async             *exec.AsyncPipeline
}

type processIOState struct {
	buffer        *RingBuffer
	pty           exec.PTY
	ptyOutputDone chan struct{}
	readCursor    int64
	// screen tracks terminal state without advancing readCursor.
	screen      *ptyScreen
	finalScreen *ScreenSnapshot
	// publishMu keeps screening and publication in one order.
	publishMu     sync.Mutex
	safePublished string
	// withheld stores masked absolute ranges. Guarded by publishMu.
	withheld   []cursorRange
	originTool string
	toolCallID string
	// failure is the run error the exit status does not carry.
	failure *hostcmd.ExecFailure
}

type processConfinementState struct {
	isCheck          bool
	sourceRevision   string
	sourceRootDigest string
	cwd              string
	runID            string
	runKey           string
	boundary         confine.Boundary
	facts            confine.SpawnFacts
	// refusalsShown counts the refusals a result has already shown.
	refusalsShown int
	// refusalNotice gathers a burst of refusals before one notice.
	refusalNotice   *time.Timer
	livenessUnknown bool
	// indexWatch is owned by the process until its completion carries it.
	indexWatch indexwatch.Snapshot
}

// cursorRange is one absolute byte range of a process's output stream.
type cursorRange struct{ start, end int64 }
