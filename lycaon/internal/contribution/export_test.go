package contribution

// conditionUsesHostFacts reports whether any node binds a host-plane fact.
func conditionUsesHostFacts(c *Condition) bool {
	if c == nil {
		return false
	}
	if c.Fact != "" {
		return facts[c.Fact].Plane == PlaneHost
	}
	for _, child := range c.All {
		if conditionUsesHostFacts(&child) {
			return true
		}
	}
	for _, child := range c.Any {
		if conditionUsesHostFacts(&child) {
			return true
		}
	}
	return conditionUsesHostFacts(c.Not)
}
