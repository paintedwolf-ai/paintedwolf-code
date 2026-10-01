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

// Registry tracks session-scoped background pipelines.
type Registry struct {
	mu        sync.Mutex
	sessions  map[string]map[string]*Process
	publish   StreamPublisher
	complete  CompletionPublisher
	refused   RefusalPublisher
	projector *captureprojection.Projector
	cfg       Config
	closed    bool
}

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
	return &Registry{
		sessions: make(map[string]map[string]*Process),
		publish:  hooks.Publish,
		complete: hooks.Complete,
		refused:  hooks.Refused,
		cfg:      cfg,
	}
}

// SetCaptureProjector installs the process-output projection.
func (r *Registry) SetCaptureProjector(projector *captureprojection.Projector) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.projector = projector
	r.mu.Unlock()
}

func (r *Registry) lookup(sessionID, handle string) (*Process, error) {
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

func (r *Registry) remove(sessionID, handle string) {
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

func (r *Registry) countRunningBackgroundLocked(sessionID string) int {
	n := 0
	for _, proc := range r.sessions[sessionID] {
		if proc.mode == JobModeBackground && proc.running {
			n++
		}
	}
	return n
}

func (r *Registry) runningConflictsLocked(sessionID, runKey string) (duplicates, awaited []string) {
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

func (r *Registry) pruneCompletedLocked(sessionID string) {
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
	if len(completed) <= r.cfg.MaxRecent {
		return
	}
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].finishedAt.Before(completed[j].finishedAt)
	})
	for _, job := range completed[:len(completed)-r.cfg.MaxRecent] {
		delete(procs, job.handle)
	}
}

func (r *Registry) countRunningAwaitedLocked(sessionID string) int {
	n := 0
	for _, proc := range r.sessions[sessionID] {
		if proc.mode == JobModeAwaited && proc.running {
			n++
		}
	}
	return n
}
