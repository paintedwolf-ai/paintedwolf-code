package progress

import "slices"

// RequestSnapshot projects the model-visible session progress state without internal UUIDs or hashes.
type RequestSnapshot struct {
	ScopeSessionID         string              `json:"scope_session_id"`
	Rows                   []Row               `json:"rows"`
	Summary                Summary             `json:"summary"`
	InitializationRequired bool                `json:"initialization_required"`
	UnaccountedResults     []UnaccountedResult `json:"unaccounted_results"`
}

// ForRequest formats snapshot for turn-state injection and update_progress results.
func ForRequest(scope string, snapshot Snapshot) RequestSnapshot {
	rows := slices.Clone(snapshot.Rows)
	if rows == nil {
		rows = []Row{}
	}
	unaccounted := []UnaccountedResult{}
	if snapshot.Reconciliation != nil && len(snapshot.Reconciliation.Unaccounted) > 0 {
		unaccounted = slices.Clone(snapshot.Reconciliation.Unaccounted)
	}
	return RequestSnapshot{
		ScopeSessionID:         scope,
		Rows:                   rows,
		Summary:                snapshot.Summary(),
		InitializationRequired: snapshot.Missing(),
		UnaccountedResults:     unaccounted,
	}
}
