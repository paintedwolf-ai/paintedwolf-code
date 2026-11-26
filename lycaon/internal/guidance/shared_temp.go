package guidance

// SharedTempSettlement records one invocation's reportable shared locations.
type SharedTempSettlement struct {
	Refused, Allowed, Declined, Unanswered []string
	Reads, Writes                          []string
	JobHandle                              string
	Tolerated, Running, ScratchAvailable   bool
}
