package guidance

// GroundingFriction is what remains of a session tree's grounding-reject
// budget after a reject: the tighter of its prompt and cycle ceilings.
type GroundingFriction struct {
	Remaining int
}

// Exhausted reports that the budget admits no further retry.
func (f GroundingFriction) Exhausted() bool {
	return f.Remaining <= 0
}
