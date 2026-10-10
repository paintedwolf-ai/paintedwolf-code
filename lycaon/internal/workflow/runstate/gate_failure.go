package runstate

func RecordGateFailure(vars map[string]any, failedLeaves []string) map[string]any {
	vars = CloneVars(vars)
	vars = SetHostVar(vars, "last_failed_leaves", append([]string(nil), failedLeaves...))
	return vars
}
