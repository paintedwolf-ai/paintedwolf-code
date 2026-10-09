package definition

const evidenceLeafPrefix = "evidence_passed:"

func PhaseEvidenceRequirements(manifest Manifest, phaseID string) []string {
	phase, ok := manifest.PhaseByID(phaseID)
	if !ok {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(leaf string) {
		leaf = strings.TrimSpace(leaf)
		if leaf == "closeout_gates_passed" {
			leaf = evidenceLeafPrefix + "verify"
		}
		if !strings.HasPrefix(leaf, evidenceLeafPrefix) {
			return
		}
		kind := strings.TrimSpace(strings.TrimPrefix(leaf, evidenceLeafPrefix))
		if kind == "test" {
			kind = "verify"
		}
		if kind == "" {
			return
		}
		if _, dup := seen[kind]; dup {
			return
		}
		seen[kind] = struct{}{}
		out = append(out, kind)
	}
	for _, gate := range phase.Gates {
		add(gate)
	}
	for _, leaf := range DecomposeGateExpression(phase.CompleteWhen) {
		add(leaf)
	}
	return out
}
