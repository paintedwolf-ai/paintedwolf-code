package progress

// PrerequisiteMissing reports whether gated work lacks a required session plan.
func PrerequisiteMissing(missing, workerChild, reviewLoopActive bool) bool {
	return !workerChild && !reviewLoopActive && missing
}

// PrerequisiteState captures session, workflow, and progress facts for gate checks.
type PrerequisiteState struct {
	SessionID        string
	RootSessionID    string
	Missing          bool
	OpenSteps        int
	WorkerChild      bool
	ReviewLoopActive bool
	Reconciliation   *Reconciliation
}
