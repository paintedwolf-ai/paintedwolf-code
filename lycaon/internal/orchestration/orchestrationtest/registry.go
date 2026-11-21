package orchestrationtest

import (
	"github.com/lycaon/lycaon/internal/orchestration"
	"testing"
)

// Registry loads the shipped agent catalog into an isolated registry.
func Registry(t testing.TB) *orchestration.MemoryAgentRegistry {
	t.Helper()
	r := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(t.Context(), r); err != nil {
		t.Fatalf("load agent registry: %v", err)
	}
	return r
}
