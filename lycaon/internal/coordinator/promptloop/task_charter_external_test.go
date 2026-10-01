package promptloop_test

func taskCallArgs(agentType, goal string) map[string]any {
	return map[string]any{
		"agent_type": agentType,
		"brief":      map[string]any{"goal": goal, "done_when": []any{"Return grounded results."}},
	}
}
