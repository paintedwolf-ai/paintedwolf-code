package workflow

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// runVarsCommitAttempts bounds re-reads when a writer outside the vars lock
// wins the compare-and-set between our load and our commit.
const runVarsCommitAttempts = 4

// RunVarsMutation modifies a run's scaffold vars under the per-run vars lock.
// Returning false skips the write; returning an error aborts the mutation.
type RunVarsMutation func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error)

// StampRunVars applies mutate to a run's scaffold vars under the per-run vars
// lock, retrying the read-modify-write when another writer bumps the revision.
// It returns the run as of the commit, or as loaded when mutate declined. The
// project dir stamped beside the row is derived from the run's session.
func (m *RunManager) StampRunVars(ctx context.Context, runID string, mutate RunVarsMutation) (*api.WorkflowRun, error) {
	return m.stampRunVars(ctx, runID, "", mutate)
}

// StampRunVarsInProject is StampRunVars for callers that carry the project dir
// from their request rather than reading it back off the session.
func (m *RunManager) StampRunVarsInProject(ctx context.Context, runID, projectDir string, mutate RunVarsMutation) (*api.WorkflowRun, error) {
	return m.stampRunVars(ctx, runID, projectDir, mutate)
}

// StampRunVarsLocked is StampRunVars for callers whose critical section is wider
// than the write itself and that already hold the run's vars lock. It still
// loads the run and vars itself, so the caller cannot supply a stale revision
// even when it loaded the run earlier for its own decisions.
func (m *RunManager) StampRunVarsLocked(ctx context.Context, runID string, mutate RunVarsMutation) (*api.WorkflowRun, error) {
	return m.stampRunVarsLocked(ctx, runID, "", mutate)
}

func (m *RunManager) stampRunVars(ctx context.Context, runID, projectDir string, mutate RunVarsMutation) (*api.WorkflowRun, error) {
	if m == nil || m.Store == nil || mutate == nil {
		return nil, nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	unlock := m.lockRunVars(runID)
	defer unlock()
	return m.stampRunVarsLocked(ctx, runID, projectDir, mutate)
}

func (m *RunManager) stampRunVarsLocked(ctx context.Context, runID, projectDir string, mutate RunVarsMutation) (*api.WorkflowRun, error) {
	if m == nil || m.Store == nil || mutate == nil {
		return nil, nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	var lastConflict error
	for attempt := 0; attempt < runVarsCommitAttempts; attempt++ {
		run, err := m.loadRun(ctx, runID)
		if errors.Is(err, ErrRunNotFound) {
			// A run canceled underneath a background stamp has no row to write.
			return nil, nil
		}
		if err != nil || run == nil {
			return nil, err
		}
		vars, err := m.Store.GetScaffoldVars(ctx, runID)
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
		err = m.Store.UpdateVars(ctx, run, dir, next)
		if err == nil {
			return run, nil
		}
		if !errors.Is(err, ErrRunRevisionConflict) {
			return nil, err
		}
		lastConflict = err
	}
	return nil, lastConflict
}

// projectDirForSession resolves the workspace path stamped beside the vars row.
func (m *RunManager) projectDirForSession(ctx context.Context, sessionID string) string {
	if m == nil || m.Sessions == nil {
		return ""
	}
	sess, err := m.Sessions.Get(ctx, strings.TrimSpace(sessionID))
	if err != nil || sess == nil {
		return ""
	}
	return sess.WorkspacePath
}

// SetRunVars replaces the vars blob wholesale under the lock, for callers that
// compute a complete replacement from the run alone.
func (m *RunManager) SetRunVars(ctx context.Context, runID string, vars map[string]any) (*api.WorkflowRun, error) {
	return m.StampRunVars(ctx, runID, func(context.Context, *api.WorkflowRun, map[string]any) (map[string]any, bool, error) {
		return vars, true, nil
	})
}
