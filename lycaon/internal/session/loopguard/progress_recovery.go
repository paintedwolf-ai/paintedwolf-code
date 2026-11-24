package loopguard

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/pkg/api"
)

// progressRecovery retains structured host snapshot remedies from a progress rejection.
type progressRecovery struct {
	code         string
	validRowIDs  map[string]struct{}
	validHandles map[string]struct{}
}

func progressRecoveryFromFailure(failure *api.InvocationFailure) progressRecovery {
	if failure == nil || failure.Details == nil {
		return progressRecovery{}
	}
	switch failure.Code {
	case "PROGRESS_ITEM_NOT_FOUND", "PROGRESS_STALE_BASIS", "PROGRESS_RESULT_HANDLE_UNKNOWN":
	default:
		return progressRecovery{}
	}
	rawSnap, present := failure.Details["snapshot"]
	if !present || rawSnap == nil {
		return progressRecovery{}
	}
	encoded, err := json.Marshal(rawSnap)
	if err != nil {
		return progressRecovery{}
	}
	var decoded struct {
		Rows               []progress.Row               `json:"rows"`
		UnaccountedResults []progress.UnaccountedResult `json:"unaccounted_results"`
		Reconciliation     *progress.Reconciliation     `json:"reconciliation"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return progressRecovery{}
	}
	// Reject scalar/non-object snapshot payloads.
	var obj map[string]any
	if err := json.Unmarshal(encoded, &obj); err != nil || obj == nil {
		return progressRecovery{}
	}
	rowIDs := make(map[string]struct{}, len(decoded.Rows))
	for _, r := range decoded.Rows {
		if id := strings.TrimSpace(r.ID); id != "" {
			rowIDs[id] = struct{}{}
		}
	}
	handles := make(map[string]struct{})
	for _, u := range decoded.UnaccountedResults {
		if h := strings.TrimSpace(u.Handle); h != "" {
			handles[h] = struct{}{}
		}
	}
	if decoded.Reconciliation != nil {
		for _, u := range decoded.Reconciliation.Unaccounted {
			if h := strings.TrimSpace(u.Handle); h != "" {
				handles[h] = struct{}{}
			}
		}
	}
	return progressRecovery{
		code:         failure.Code,
		validRowIDs:  rowIDs,
		validHandles: handles,
	}
}

func (r progressRecovery) matches(args map[string]any) bool {
	if r.code == "" {
		return false
	}
	m, err := progress.PrepareMutation(args)
	if err != nil {
		return false
	}
	switch r.code {
	case "PROGRESS_ITEM_NOT_FOUND", "PROGRESS_STALE_BASIS":
		if !m.HasRowEdits() {
			return false
		}
		if len(m.Replace) > 0 {
			return true
		}
		for _, id := range m.Remove {
			if _, ok := r.validRowIDs[id]; !ok {
				return false
			}
		}
		for _, u := range m.Update {
			if _, ok := r.validRowIDs[u.ID]; !ok {
				return false
			}
		}
		for _, a := range m.Add {
			if a.AfterID != "" && a.AfterID != "start" {
				if _, ok := r.validRowIDs[a.AfterID]; !ok {
					return false
				}
			}
		}
		return true
	case "PROGRESS_RESULT_HANDLE_UNKNOWN":
		if len(r.validHandles) == 0 {
			return len(m.AcknowledgeResults) == 0
		}
		if len(m.AcknowledgeResults) == 0 {
			return false
		}
		for _, h := range m.AcknowledgeResults {
			if _, ok := r.validHandles[h]; !ok {
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
		resolved = !progress.PrerequisiteMissing(state.Missing, state.WorkerChild, state.ReviewLoopActive)
	case "PROGRESS_RECONCILIATION_REQUIRED":
		resolved = state.WorkerChild || state.Reconciliation == nil
	}
	if resolved {
		return code, nil
	}
	return "", nil
}
