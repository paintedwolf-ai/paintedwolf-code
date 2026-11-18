package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCloudflareWorkersAIReportsToolCallCapability(t *testing.T) {
	t.Parallel()
	catalog := liveCatalogForShipIDs(t, "cloudflare-workers-ai")
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")), nil)
	if err == nil {
		registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	}
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)

	var toolCalls bool
	for _, p := range registry.List(t.Context()) {
		if p.ID == "cloudflare-workers-ai" {
			toolCalls = p.Features.ToolCalls
			break
		}
	}
	if !toolCalls {
		t.Fatal("cloudflare-workers-ai should report tool-call capability")
	}
}
