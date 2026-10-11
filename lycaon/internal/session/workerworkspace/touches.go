package workerworkspace

import (
	"sort"
	"strings"
	"sync"

	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
)

// TouchLedger records live repo-relative paths touched by in-flight worker jobs.
type TouchLedger struct {
	mu sync.RWMutex
	// Released: ClearJob drops a job's paths when the job ends or is rewound
	// away, which is the only point at which they stop describing live work.
	byJob map[string]map[string]struct{} // jobID -> repo-relative paths
}

// NewTouchLedger creates an empty touch ledger.
func NewTouchLedger() *TouchLedger {
	return &TouchLedger{byJob: make(map[string]map[string]struct{})}
}

// RecordTouch records a repo-relative path touched by a worker job.
func (l *TouchLedger) RecordTouch(jobID, relPath string) {
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
func (l *TouchLedger) Paths(jobID string) []string {
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
func (l *TouchLedger) ClearJob(jobID string) {
	if l == nil {
		return
	}
	jobID = strings.TrimSpace(jobID)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byJob, jobID)
}
