package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type preparationCapture struct {
	thinkingCaptureProvider
	prepared []modelcall.CompletionRequest
}

func (p *preparationCapture) PrepareContext(_ context.Context, req modelcall.CompletionRequest) (modelcall.ContextPreparation, error) {
	p.prepared = append(p.prepared, req)
	return modelcall.ContextPreparation{PreparedTokens: 2}, nil
}

func TestContextPreparationUsesOnlyConfiguredLocalProvider(t *testing.T) {
	for _, kind := range []string{"nativegen", "openai-compatible"} {
		t.Run(kind, func(t *testing.T) {
			capture := &preparationCapture{}
			registry := newTestRegistry()
			registry.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
				Providers:  map[string]modelcall.Provider{"p": capture},
				Entries:    map[string]CatalogEntry{"p": {ID: "p", Kind: kind}},
				Configured: map[string]bool{"p": true},
			}))
			registry.SetOutboundSecretScreen(NewModelSecretScreen(modelScreenMatcher(t), nil))
			result, err := registry.PrepareContext(t.Context(), &ModelSelection{ProviderID: "p", Model: "m"}, modelcall.CompletionRequest{
				Messages: []api.Message{{Role: api.MessageRoleSystem, Content: "Instructions", PromptCacheBreakpoint: api.PromptCacheTierStanding}},
			})
			testutil.FailErr(t, "prepare selected context", err)
			if len(capture.calls) != 0 {
				t.Fatal("preparation generated a completion")
			}
			if kind == "nativegen" {
				if result.PreparedTokens != 2 || len(capture.prepared) != 1 || capture.prepared[0].Model != "m" {
					t.Fatalf("preparation = %+v; calls = %d", result, len(capture.prepared))
				}
			} else if result.PreparedTokens != 0 || len(capture.prepared) != 0 {
				t.Fatal("preparation contacted an external provider")
			}
		})
	}
}

func TestPreparationScreenNeverAsksOrAdopts(t *testing.T) {
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		t.Fatal("preparation asked for approval")
		return secretmatch.Resolution{}, nil
	})
	screen.SetManagedSecretAdopter(func(context.Context, ManagedSecretAdoptRequest) (string, error) {
		t.Fatal("preparation adopted a secret")
		return "", nil
	})
	allowed, err := screen.ScreenPreparation(t.Context(), modelSecretRequest())
	testutil.FailErr(t, "inspect protected candidate", err)
	if allowed {
		t.Fatal("protected candidate was prepared")
	}
	allowed, err = screen.ScreenPreparation(t.Context(), modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Ordinary text"}}})
	testutil.FailErr(t, "inspect clear candidate", err)
	if !allowed {
		t.Fatal("clear candidate was skipped")
	}
}

func TestPreparationScreenRefreshFailureSkipsCandidate(t *testing.T) {
	screen := NewModelSecretScreen(modelScreenMatcher(t), nil)
	failure := errors.New("unavailable evidence")
	screen.SetManagedSecretEvidence(func(_ context.Context, project, root string) ([]secretmatch.Remembered, error) {
		if project != "proj-1" || root != "sess-1" {
			t.Fatalf("evidence scope = %q/%q", project, root)
		}
		return nil, failure
	})
	allowed, err := screen.ScreenPreparation(t.Context(), modelSecretRequest())
	if allowed || !errors.Is(err, failure) {
		t.Fatalf("evidence failure: allowed=%v error=%v", allowed, err)
	}
}
