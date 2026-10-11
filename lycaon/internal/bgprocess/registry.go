package bgprocess

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/indexwatch"
	"github.com/lycaon/lycaon/pkg/api"
)

const DefaultMaxBackground = 4

const DefaultMaxRecent = 16

// DefaultMaxAwaited bounds intentional overlap in the awaited command lane.
const DefaultMaxAwaited = 4

var (
	ErrBackgroundCapReached = errors.New("background process cap reached")
	ErrAwaitedCapReached    = errors.New("awaited command concurrency cap reached")
	ErrProcessNotFound      = errors.New("background process not found")
	ErrProcessNotRunning    = errors.New("background process is not running")
)

// StreamPublisher emits incremental background process output over SSE.
type StreamPublisher func(ctx context.Context, projectID, sessionID string, ev api.BackgroundProcessEvent)

// Config tunes per-session background process limits.
type Config struct {
	MaxBackground   int
	MaxAwaited      int
	MaxRecent       int
	RingBufferBytes int
}

func DefaultConfig() Config {
	return Config{
		MaxBackground:   DefaultMaxBackground,
		MaxAwaited:      DefaultMaxAwaited,
		MaxRecent:       DefaultMaxRecent,
		RingBufferBytes: DefaultRingBufferBytes,
	}
}

// Registry composes session-scoped process services and launches pipelines.
type Registry struct {
	jobs            *processTable
	ringBufferBytes int
	Terminal        *Terminal
	Output          *Output
	Lifecycle       *ProcessLifecycle
}

// processTable serializes admission, process state, and teardown across services.
type processTable struct {
	mu            sync.Mutex
	sessions      map[string]map[string]*Process
	closed        bool
	maxBackground int
	maxAwaited    int
	maxRecent     int
}

// Terminal owns held PTY interaction and sealed captures.
type Terminal struct {
	jobs            *processTable
	ringBufferBytes int
	Output          *Output
	Lifecycle       *ProcessLifecycle
}

// Output owns capture screening, projections, and observer publication.
type Output struct {
	jobs            *processTable
	ringBufferBytes int
	publish         StreamPublisher
	complete        CompletionPublisher
	refused         RefusalPublisher
	projector       *captureprojection.Projector
}

// ProcessLifecycle owns settlement, termination, and resource release.
type ProcessLifecycle struct{ jobs *processTable }

var ErrRegistryClosed = errors.New("background registry closed")

// NewRegistry constructs an empty background process registry.
func NewRegistry(cfg Config, hooks Hooks) *Registry {
	if cfg.MaxBackground <= 0 {
		cfg.MaxBackground = DefaultMaxBackground
	}
	if cfg.MaxAwaited <= 0 {
		cfg.MaxAwaited = DefaultMaxAwaited
	}
	if cfg.RingBufferBytes <= 0 {
		cfg.RingBufferBytes = DefaultRingBufferBytes
	}
	if cfg.MaxRecent <= 0 {
		cfg.MaxRecent = DefaultMaxRecent
	}
	jobs := &processTable{sessions: make(map[string]map[string]*Process), maxBackground: cfg.MaxBackground, maxAwaited: cfg.MaxAwaited, maxRecent: cfg.MaxRecent}
	output := &Output{jobs: jobs, ringBufferBytes: cfg.RingBufferBytes, publish: hooks.Publish, complete: hooks.Complete, refused: hooks.Refused}
	lifecycle := &ProcessLifecycle{jobs: jobs}
	terminal := &Terminal{jobs: jobs, ringBufferBytes: cfg.RingBufferBytes, Output: output, Lifecycle: lifecycle}
	return &Registry{jobs: jobs, ringBufferBytes: cfg.RingBufferBytes, Terminal: terminal, Output: output, Lifecycle: lifecycle}
}

// SetCaptureProjector installs the process-output projection.
func (r *Output) SetCaptureProjector(projector *captureprojection.Projector) {
	if r == nil {
		return
	}
	r.jobs.mu.Lock()
	r.projector = projector
	r.jobs.mu.Unlock()
}

func (r *processTable) lookup(sessionID, handle string) (*Process, error) {
	if r == nil {
		return nil, fmt.Errorf("background registry not configured")
	}
	sessionID = trim(sessionID)
	handle = trim(handle)
	r.mu.Lock()
	defer r.mu.Unlock()
	procs := r.sessions[sessionID]
	if procs == nil {
		return nil, ErrProcessNotFound
	}
	proc, ok := procs[handle]
	if !ok {
		return nil, ErrProcessNotFound
	}
	return proc, nil
}

func (r *processTable) remove(sessionID, handle string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	procs := r.sessions[sessionID]
	if procs == nil {
		return
	}
	if proc := procs[handle]; proc != nil {
		proc.indexWatch.Release()
		proc.indexWatch = indexwatch.Snapshot{}
	}
	delete(procs, handle)
	if len(procs) == 0 {
		delete(r.sessions, sessionID)
	}
}

func (r *processTable) countRunningBackgroundLocked(sessionID string) int {
	n := 0
	for _, proc := range r.sessions[sessionID] {
		if proc.mode == JobModeBackground && proc.running {
			n++
		}
	}
	return n
}

func (r *processTable) runningConflictsLocked(sessionID, runKey string) (duplicates, awaited []string) {
	for _, proc := range r.sessions[sessionID] {
		if !proc.running || proc.kind != processKindPipeline {
			continue
		}
		if proc.runKey == runKey {
			duplicates = append(duplicates, proc.Handle)
		}
		if proc.mode == JobModeAwaited {
			awaited = append(awaited, proc.Handle)
		}
	}
	sort.Strings(duplicates)
	sort.Strings(awaited)
	return duplicates, awaited
}

func (r *processTable) pruneCompletedLocked(sessionID string) {
	procs := r.sessions[sessionID]
	if len(procs) == 0 {
		return
	}
	type completedJob struct {
		handle     string
		finishedAt time.Time
	}
	var completed []completedJob
	for handle, proc := range procs {
		if !proc.running && proc.hasExit {
			completed = append(completed, completedJob{handle: handle, finishedAt: proc.finishedAt})
		}
	}
	if len(completed) <= r.maxRecent {
		return
	}
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].finishedAt.Before(completed[j].finishedAt)
	})
	for _, job := range completed[:len(completed)-r.maxRecent] {
		delete(procs, job.handle)
	}
}

func (r *processTable) countRunningAwaitedLocked(sessionID string) int {
	n := 0
	for _, proc := range r.sessions[sessionID] {
		if proc.mode == JobModeAwaited && proc.running {
			n++
		}
	}
	return n
}
