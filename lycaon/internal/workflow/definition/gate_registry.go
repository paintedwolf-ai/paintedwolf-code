package definition

// GatePredicateRef locates a gate leaf referenced from a shipped manifest.
type GatePredicateRef struct {
	ManifestID string
	PhaseID    string
	LeafID     string
}
