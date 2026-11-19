package guidance

// SharedTempSettlement records one invocation's reportable shared locations.
type SharedTempSettlement struct {
	Refused, Allowed, Declined, Unanswered []string
	Tolerated, Running, ScratchAvailable   bool
}
