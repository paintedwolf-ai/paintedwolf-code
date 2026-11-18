package guidance

// PlanPhaseNames returns the spec-posture milestone labels (plan gate phases 0–6).
func PlanPhaseNames() []string {
	return append([]string(nil), planPhaseNames...)
}
