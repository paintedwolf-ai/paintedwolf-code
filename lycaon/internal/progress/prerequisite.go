package progress

// PrerequisiteMissing preserves the existing worker and workflow review-loop
// exemptions. Content validity comes from the existing progress parser.
func PrerequisiteMissing(content string, workerChild, reviewLoopActive bool) bool {
	return !workerChild && !reviewLoopActive && ProgressMissing(content)
}
