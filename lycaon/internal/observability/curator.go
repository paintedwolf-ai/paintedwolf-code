package observability

// CuratorEvent is one altitude curator invocation for session debug capture.
type CuratorEvent struct {
	Tool      string
	View      string
	Target    string
	Selected  int
	Total     int
	Dropped   int
	Retries   int
	Fallback  bool
	CacheHit  bool
	SessionID string
}

// LogCurator emits a structured slog row for curator coverage and failures.
func LogCurator(ev CuratorEvent) {
	logCurator(ev)
}
