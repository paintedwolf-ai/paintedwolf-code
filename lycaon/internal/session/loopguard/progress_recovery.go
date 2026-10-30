package loopguard

import (
	"encoding/json"

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
