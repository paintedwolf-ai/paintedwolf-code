package providerwire

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/observability"
)

func TestPromptCacheWarningsSeparateProviderModelAndPurpose(t *testing.T) {
	var tracker promptCacheHitTracker
	base := observability.PromptCacheScope{SessionID: "session", ProviderID: "provider", Model: "model", Purpose: "stream"}
	scopes := []observability.PromptCacheScope{base,
		{SessionID: "session", ProviderID: "other", Model: "model", Purpose: "stream"},
		{SessionID: "session", ProviderID: "provider", Model: "other", Purpose: "stream"},
		{SessionID: "session", ProviderID: "provider", Model: "model", Purpose: "title"},
	}
	for _, scope := range scopes {
		if warning := tracker.note(scope, modelcall.CompletionRequest{Messages: tieredMessages()}, providerprofile.PromptCachePolicy{Mode: providerprofile.PromptCacheAutomaticPrefix}, modelcall.TokenUsage{PromptTokens: 4096}, time.Unix(0, 0), time.Unix(1, 0), false); warning.Comparison != "first_request" {
			t.Fatal("unrelated requests shared warning history")
		}
	}
	if observation := tracker.note(base, modelcall.CompletionRequest{Messages: tieredMessages()}, providerprofile.PromptCachePolicy{Mode: providerprofile.PromptCacheAutomaticPrefix}, modelcall.TokenUsage{PromptTokens: 4096}, time.Unix(2, 0), time.Unix(3, 0), false); observation.Comparison != "comparable" || observation.Window.Requests != 1 {
		t.Fatalf("comparable request lost its own history: %+v", observation)
	}
}
