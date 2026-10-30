package loopguard

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/pkg/api"
)

// progressRecovery retains only a host-issued basis from a typed rejection.
type progressRecovery struct {
	revision       string
	token          string
	reconciliation bool
}

func progressRecoveryFromFailure(failure *api.InvocationFailure) progressRecovery {
	if failure == nil {
		return progressRecovery{}
	}
	switch failure.Code {
	case "PROGRESS_REVISION_REQUIRED", "PROGRESS_REVISION_CHANGED", "PROGRESS_RECONCILIATION_CHANGED":
	default:
		return progressRecovery{}
	}
	raw, err := json.Marshal(failure.Details["snapshot"])
	if err != nil {
		return progressRecovery{}
	}
	var snapshot progress.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return progressRecovery{}
	}
	recovery := progressRecovery{revision: snapshot.Revision, reconciliation: failure.Code == "PROGRESS_RECONCILIATION_CHANGED"}
	if snapshot.Reconciliation != nil {
		recovery.token = snapshot.Reconciliation.Token
	}
	return recovery
}

func (r progressRecovery) matches(args map[string]any) bool {
	revision, _ := args["expected_revision"].(string)
	if r.revision == "" || revision != r.revision {
		return false
	}
	token, _ := args["reconciliation_token"].(string)
	return !r.reconciliation || token == r.token
}

// SetProgressPrerequisiteResolver connects current prerequisites from the root session.
func (g *MemoryDoomLoopGuard) SetProgressPrerequisiteResolver(resolve func(context.Context, string) (progress.PrerequisiteState, error)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.progressState = resolve
}

func (g *MemoryDoomLoopGuard) resolvedProgressPrerequisite(ctx context.Context, sessionID, key string) (string, error) {
	g.mu.RLock()
	resolve := g.progressState
	states, _ := g.states.Load(sessionID)
	code := ""
	if state := states[key]; state != nil {
		code = state.lastCode
	}
	g.mu.RUnlock()
	if resolve == nil || code != "PROGRESS_MISSING" && code != "PROGRESS_RECONCILIATION_REQUIRED" {
		return "", nil
	}
	state, err := resolve(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if state.SessionID != sessionID || state.RootSessionID == "" {
		return "", fmt.Errorf("progress prerequisite state does not match session")
	}
	resolved := false
	switch code {
	case "PROGRESS_MISSING":
		resolved = !progress.PrerequisiteMissing(state.Content, state.WorkerChild, state.ReviewLoopActive)
	case "PROGRESS_RECONCILIATION_REQUIRED":
		resolved = state.WorkerChild || state.Reconciliation == nil
	}
	if resolved {
		return code, nil
	}
	return "", nil
}
