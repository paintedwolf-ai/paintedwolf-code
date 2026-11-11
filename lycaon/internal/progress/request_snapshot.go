package progress

// RequestSnapshot identifies the authoritative checklist scope and its next action.
type RequestSnapshot struct {
	Snapshot
	Summary                Summary `json:"summary"`
	ScopeSessionID         string  `json:"scope_session_id"`
	InitializationRequired bool    `json:"initialization_required"`
}

func ForRequest(scope string, snapshot Snapshot) RequestSnapshot {
	return RequestSnapshot{Snapshot: snapshot, Summary: Summarize(snapshot.Content), ScopeSessionID: scope, InitializationRequired: ProgressMissing(snapshot.Content)}
}
