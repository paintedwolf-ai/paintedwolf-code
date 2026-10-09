package runstate

func VerdictOperationPending(status string) bool {
	return status == "prepared" || status == "evidence_applied"
}
