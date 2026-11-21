package prompts

// ReadOnlyAgents retains profiles that declare read-only authority.
func ReadOnlyAgents(agents []string) []string {
	out := make([]string, 0, len(agents))
	for _, agent := range agents {
		if writes, known := AgentMutationCapable(agent); known && !writes {
			out = append(out, agent)
		}
	}
	return out
}
