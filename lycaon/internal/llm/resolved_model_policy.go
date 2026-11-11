package llm

// ResolveModelPolicy applies runtime defaults without changing stored assignments.
func ResolveModelPolicy(policy ModelPolicy, localAvailable bool) ModelPolicy {
	resolved := cloneModelPolicy(policy)
	resolved.Coordinator = CoordinatorRefWithLocalDefault(policy, localAvailable)
	resolved.Lite = SummarizerRefWithLocalDefault(policy, localAvailable)
	return resolved
}
