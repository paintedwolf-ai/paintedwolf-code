package webresearch

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
)

// The warmer only seeds off the coordinator's provider when the lite role
// shares it; a dedicated lite provider means the summarizer is its own budget.
func TestWarmSummarizerSharesCoordinatorFromPolicy(t *testing.T) {
	dedicated := llm.NewInMemoryPolicyStore(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "openai", Model: "gpt-4o"},
		Lite:        llm.ModelRef{ProviderID: "ollama", Model: "llama3.1"},
	})
	w := NewWarmer(nil, nil, dedicated, nil)
	if w.summarizerSharesCoordinator("") {
		t.Fatal("want dedicated lite provider")
	}

	shared := llm.NewInMemoryPolicyStore(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "openai", Model: "gpt-4o"},
		Lite:        llm.ModelRef{ProviderID: "openai", Model: "gpt-4o-mini"},
	})
	wShared := NewWarmer(nil, nil, shared, nil)
	if !wShared.summarizerSharesCoordinator("") {
		t.Fatal("want shared when lite uses coordinator provider")
	}
}
