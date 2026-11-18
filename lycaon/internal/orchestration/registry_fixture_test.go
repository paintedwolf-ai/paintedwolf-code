package orchestration

import "testing"

func agentRegistryFixture(t testing.TB) *MemoryAgentRegistry {
	t.Helper()
	r := NewMemoryAgentRegistry()
	if err := LoadRequiredAgentRegistry(t.Context(), r); err != nil {
		t.Fatalf("load agent registry: %v", err)
	}
	return r
}
