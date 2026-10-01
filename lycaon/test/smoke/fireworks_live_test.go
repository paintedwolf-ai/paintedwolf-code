//go:build live_llm

package smoke_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// An unset model ID selects live serverless discovery.
func TestFireworksLivePing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Fireworks live test in short mode")
	}
	apiKey := os.Getenv("FIREWORKS_API_KEY")
	if apiKey == "" {
		t.Skip("FIREWORKS_API_KEY not set")
	}

	cfg, err := llm.LoadProviderConfig()
	testutil.FailErr(t, "llm.LoadProviderConfig failed", err)
	var entry *llm.ProviderEntry
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "fireworks" {
			entry = &cfg.Providers[i]
			break
		}
	}
	if entry == nil {
		t.Fatal("fireworks not in bundled providers.yaml")
	}

	model := resolveFireworksLiveModel(t, entry, apiKey)
	if model == "" {
		t.Skip("no Fireworks model available (set FIREWORKS_MODEL or ensure serverless discovery returns models)")
	}

	provider := openaicompat.New("fireworks", entry.BaseURL, apiKey, entry.Models)
	completion, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Model: model,
		Messages: []api.Message{{
			Role:    api.MessageRoleUser,
			Content: "Reply with exactly: pong",
		}},
	})
	testutil.FailErr(t, "provider.Complete failed", err)
	if completion.Content == "" && len(completion.ToolCalls) == 0 {
		t.Fatal("empty completion from Fireworks")
	}
}

func resolveFireworksLiveModel(t *testing.T, entry *llm.ProviderEntry, apiKey string) string {
	t.Helper()
	if len(entry.Models) > 0 {
		if id := entry.Models[0].ID; id != "" {
			return id
		}
	}
	if id := os.Getenv("FIREWORKS_MODEL"); id != "" {
		return id
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ids, err := discovery.FireworksModels(ctx, entry.BaseURL, apiKey, nil)
	testutil.FailErr(t, "DiscoverFireworksServerlessModels", err)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}
