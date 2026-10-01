package session

import (
	"sort"
	"strings"
	"sync"

	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
)

// WorkerTouchLedger records live repo-relative paths touched by in-flight worker jobs.
type WorkerTouchLedger struct {
	mu sync.RWMutex
	// Released: ClearJob drops a job's paths when the job ends or is rewound
	// away, which is the only point at which they stop describing live work.
	byJob map[string]map[string]struct{} // jobID -> repo-relative paths
}

// NewWorkerTouchLedger creates an empty touch ledger.
func NewWorkerTouchLedger() *WorkerTouchLedger {
	return &WorkerTouchLedger{byJob: make(map[string]map[string]struct{})}
}

// SetWorkerTouchLedger wires the live touch ledger for worker write hooks and board enrich.
func (m *Manager) SetWorkerTouchLedger(l *WorkerTouchLedger) {
	if m == nil {
		return
	}
	m.workerTouches = l
}

// WorkerTouchedPaths returns sorted touched paths for a worker job (board.WorkerTouchEnricher).
func (m *Manager) WorkerTouchedPaths(jobID string) []string {
	if m == nil || m.workerTouches == nil {
		return nil
	}
	return m.workerTouches.Paths(jobID)
}

// RecordTouch records a repo-relative path touched by a worker job.
func (l *WorkerTouchLedger) RecordTouch(jobID, relPath string) {
	if l == nil {
		return
	}
	jobID = strings.TrimSpace(jobID)
	relPath = sessioncheckpoint.NormalizePath(relPath)
	if jobID == "" || relPath == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byJob == nil {
		l.byJob = make(map[string]map[string]struct{})
	}
	if l.byJob[jobID] == nil {
		l.byJob[jobID] = make(map[string]struct{})
	}
	l.byJob[jobID][relPath] = struct{}{}
}

// Paths returns sorted touched paths for jobID.
func (l *WorkerTouchLedger) Paths(jobID string) []string {
	if l == nil {
		return nil
	}
	jobID = strings.TrimSpace(jobID)
	l.mu.RLock()
	defer l.mu.RUnlock()
	paths := l.byJob[jobID]
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for p := range paths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ClearJob removes all touches for a promoted or canceled worker job.
func (l *WorkerTouchLedger) ClearJob(jobID string) {
	if l == nil {
		return
	}
	jobID = strings.TrimSpace(jobID)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byJob, jobID)
}
