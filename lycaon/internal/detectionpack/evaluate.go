package detectionpack

// ToolExecEvaluation is the named matcher outcome for one host-derived
// tool_exec observation. Callers compose this once per outer action.
type ToolExecEvaluation struct {
	Match Match
	OK    bool
}

// EvaluateToolExec builds the event and returns the deterministic winner.
func EvaluateToolExec(m *Matcher, obs ActionObservation) ToolExecEvaluation {
	if m == nil {
		return ToolExecEvaluation{}
	}
	hit, ok := m.Match(NewEvent(obs))
	return ToolExecEvaluation{Match: hit, OK: ok}
}
