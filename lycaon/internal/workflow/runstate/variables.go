package runstate

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

type VarsReader interface {
	Get(context.Context, string) (*api.WorkflowRun, error)
	GetScaffoldVars(context.Context, string) (map[string]any, error)
}

type VarsWriter interface {
	UpdateVars(context.Context, *api.WorkflowRun, string, map[string]any) error
}

type SessionReader interface {
	Get(context.Context, string) (*api.Session, error)
}

type Variables struct {
	reads    VarsReader
	writes   VarsWriter
	sessions SessionReader
	guards   sync.Map
}

func NewVariables(reads VarsReader, writes VarsWriter, sessions SessionReader) *Variables {
	return &Variables{reads: reads, writes: writes, sessions: sessions}
}

func (m *Variables) Lock(runID string) func() {
	value, _ := m.guards.LoadOrStore(runID, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func (m *Variables) LockOnce(runID string) func() {
	unlock := m.Lock(runID)
	var once sync.Once
	return func() { once.Do(unlock) }
}

// runVarsCommitAttempts bounds re-reads when a writer outside the vars lock
// wins the compare-and-set between our load and our commit.
const runVarsCommitAttempts = 4

// VarsMutation modifies a run's scaffold vars under the per-run vars lock.
// Returning false skips the write; returning an error aborts the mutation.
type VarsMutation func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error)

// Stamp applies mutate to a run's scaffold vars under the per-run vars
// lock, retrying the read-modify-write when another writer bumps the revision.
// It returns the run as of the commit, or as loaded when mutate declined. The
// project dir stamped beside the row is derived from the run's session.
func (m *Variables) Stamp(ctx context.Context, runID string, mutate VarsMutation) (*api.WorkflowRun, error) {
	return m.stamp(ctx, runID, "", mutate)
}

// StampInProject is Stamp for callers that carry the project dir
// from their request rather than reading it back off the session.
func (m *Variables) StampInProject(ctx context.Context, runID, projectDir string, mutate VarsMutation) (*api.WorkflowRun, error) {
	return m.stamp(ctx, runID, projectDir, mutate)
}

// StampLocked is Stamp for callers whose critical section is wider
// than the write itself and that already hold the run's vars lock. It still
// loads the run and vars itself, so the caller cannot supply a stale revision
// even when it loaded the run earlier for its own decisions.
func (m *Variables) StampLocked(ctx context.Context, runID string, mutate VarsMutation) (*api.WorkflowRun, error) {
	return m.stampLocked(ctx, runID, "", mutate)
}

func (m *Variables) stamp(ctx context.Context, runID, projectDir string, mutate VarsMutation) (*api.WorkflowRun, error) {
	if m == nil || m.reads == nil || m.writes == nil || mutate == nil {
		return nil, nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	unlock := m.Lock(runID)
	defer unlock()
	return m.stampLocked(ctx, runID, projectDir, mutate)
}

func (m *Variables) stampLocked(ctx context.Context, runID, projectDir string, mutate VarsMutation) (*api.WorkflowRun, error) {
	if m == nil || m.reads == nil || m.writes == nil || mutate == nil {
		return nil, nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	var lastConflict error
	for attempt := 0; attempt < runVarsCommitAttempts; attempt++ {
		run, err := m.reads.Get(ctx, runID)
		if errors.Is(err, ErrNotFound) {
			// A run canceled underneath a background stamp has no row to write.
			return nil, nil
		}
		if err != nil || run == nil {
			return nil, err
		}
		vars, err := m.reads.GetScaffoldVars(ctx, runID)
		if err != nil {
			return nil, err
		}
		next, changed, err := mutate(ctx, run, vars)
		if err != nil {
			return nil, err
		}
		if !changed {
			return run, nil
		}
		dir := projectDir
		if strings.TrimSpace(dir) == "" {
			dir = m.projectDirForSession(ctx, run.SessionID)
		}
		err = m.writes.UpdateVars(ctx, run, dir, next)
		if err == nil {
			return run, nil
		}
		if !errors.Is(err, ErrRevisionConflict) {
			return nil, err
		}
		lastConflict = err
	}
	return nil, lastConflict
}

// projectDirForSession resolves the workspace path stamped beside the vars row.
func (m *Variables) projectDirForSession(ctx context.Context, sessionID string) string {
	if m == nil || m.sessions == nil {
		return ""
	}
	sess, err := m.sessions.Get(ctx, strings.TrimSpace(sessionID))
	if err != nil || sess == nil {
		return ""
	}
	return sess.WorkspacePath
}

// Set replaces the vars blob wholesale under the lock, for callers that
// compute a complete replacement from the run alone.
func (m *Variables) Set(ctx context.Context, runID string, vars map[string]any) (*api.WorkflowRun, error) {
	return m.Stamp(ctx, runID, func(context.Context, *api.WorkflowRun, map[string]any) (map[string]any, bool, error) {
		return vars, true, nil
	})
}

func (m *Variables) Forget(runID string) {
	if m != nil {
		m.guards.Delete(runID)
	}
}
