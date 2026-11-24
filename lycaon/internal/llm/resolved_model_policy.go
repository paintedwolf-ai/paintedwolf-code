package llm

// ResolveModelPolicy applies runtime defaults without changing stored assignments.
func ResolveModelPolicy(policy ModelPolicy, model string) ModelPolicy {
	resolved := cloneModelPolicy(policy)
	resolved.Coordinator = CoordinatorRefWithLocalDefault(policy, model)
	resolved.Lite = SummarizerRefWithLocalDefault(policy, model)
	for i, ref := range resolved.AgentPool.Models {
		resolved.AgentPool.Models[i] = selectedLocalRef(ref, model)
	}
	return resolved
}
