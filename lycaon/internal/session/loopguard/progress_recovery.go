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
	itemIDs        map[string]struct{}
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
	var itemIDs map[string]struct{}
	if len(snapshot.ItemIDs) > 0 {
		itemIDs = make(map[string]struct{}, len(snapshot.ItemIDs))
		for _, id := range snapshot.ItemIDs {
			if id != "" {
				itemIDs[id] = struct{}{}
			}
		}
	}
	recovery := progressRecovery{revision: snapshot.Revision, itemIDs: itemIDs, reconciliation: failure.Code == "PROGRESS_RECONCILIATION_CHANGED"}
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
	if len(r.itemIDs) > 0 && !recoveryItemIDsMatch(args["item_ids"], r.itemIDs) {
		return false
	}
	token, _ := args["reconciliation_token"].(string)
	return !r.reconciliation || token == r.token
}

func recoveryItemIDsMatch(raw any, allowed map[string]struct{}) bool {
	switch v := raw.(type) {
	case []string:
		for _, id := range v {
			if id == "" {
				continue
			}
			if _, ok := allowed[id]; !ok {
				return false
			}
		}
		return true
	case []any:
		for _, item := range v {
			switch s := item.(type) {
			case nil:
			case string:
				if s == "" {
					continue
				}
				if _, ok := allowed[s]; !ok {
					return false
				}
			default:
				return false
			}
		}
		return true
	default:
		return false
	}
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
