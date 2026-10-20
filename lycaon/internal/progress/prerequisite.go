package progress

// PrerequisiteMissing preserves the existing worker and workflow review-loop
// exemptions. Content validity comes from the existing progress parser.
func PrerequisiteMissing(content string, workerChild, reviewLoopActive bool) bool {
	return !workerChild && !reviewLoopActive && ProgressMissing(content)
}

// PrerequisiteState comes from current session/workflow facts and the existing
// root-session progress store. No field is inferred from command or chat prose.
type PrerequisiteState struct {
	SessionID        string
	RootSessionID    string
	Content          string
	WorkerChild      bool
	ReviewLoopActive bool
}
