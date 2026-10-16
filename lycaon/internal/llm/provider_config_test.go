package llm

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadProviderConfigRejectsUnknownFields(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{
		config.Providers: "providers:\n  - id: test\n    knid: test\n",
	})
	if _, err := LoadProviderConfig(); err == nil {
		t.Fatal("unknown provider field was accepted")
	}
}

func TestProviderCatalogRejectsUnknownLocalFields(t *testing.T) {
	stageShipProviders(t, "providers: []\n")
	localPath := filepath.Join(t.TempDir(), "providers.local.yaml")
	writeProvidersLocal(t, localPath, []byte("providers:\n  - id: test\n    knid: test\n"))
	if _, err := NewProviderCatalogAt(localPath); err == nil {
		t.Fatal("unknown local provider field was accepted")
	}
}

func TestProviderCatalogRejectsLocalRetryProfileWithoutKindTemplate(t *testing.T) {
	stageShipProviders(t, "providers: []\n")
	localPath := filepath.Join(t.TempDir(), "providers.local.yaml")
	writeProvidersLocal(t, localPath, []byte("providers:\n  - id: test\n    http_retry_profile: hosted\n"))
	if _, err := NewProviderCatalogAt(localPath); err == nil {
		t.Fatal("local retry profile selection was accepted")
	}
}

// The Ollama kind ships its parser rejection grammars, and a live instance
// built from the catalog carries them into the driver.
func TestOllamaKindAttachesShippedToolCallRejections(t *testing.T) {
	cfg, err := LoadProviderConfig()
	testutil.FailErr(t, "load provider catalog", err)
	ollama := providerByID(cfg.Providers, "ollama")
	if ollama == nil || ollama.ToolCallRejections.Profile != "ollama" || ollama.ToolCallRejections.IsZero() {
		t.Fatalf("ollama kind policy = %+v", ollama)
	}
	for _, provider := range cfg.Providers {
		if provider.ToolCallRejectionProfile != "" && provider.Kind != "ollama" {
			t.Errorf("provider %s declares parser rejections without a driver that reads them", provider.ID)
		}
	}
	entry, err := resolveLocalEntry(ProviderEntry{ID: "desktop", Kind: "ollama", BaseURL: "http://localhost:11434/v1"}, map[string]ProviderEntry{"ollama": *ollama})
	testutil.FailErr(t, "resolve local ollama", err)
	p, err := newProviderForEntry(context.Background(), entry, "", entry.HTTPRetry, nil, nil)
	testutil.FailErr(t, "build ollama driver", err)
	cause := failure.ErrProviderToolCallUnparsed
	if typed := p.Profile().ToolCallRejections.Classify("desktop", "m", "unexpected end of JSON input", true, cause); typed == cause {
		t.Fatal("driver lacks the kind's parser rejection grammar")
	}
}

func TestToolCallRejectionProfilesBelongToTheKindCatalog(t *testing.T) {
	ship := map[string]ProviderEntry{"ollama": {Kind: "ollama", ToolCallRejectionProfile: "ollama"}}
	if _, err := resolveLocalEntry(ProviderEntry{ID: "desktop", Kind: "ollama", ToolCallRejectionProfile: "custom"}, ship); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched local profile: %v", err)
	}
	if _, err := resolveLocalEntry(ProviderEntry{ID: "other", Kind: "unknown", ToolCallRejectionProfile: "ollama"}, ship); err == nil || !strings.Contains(err.Error(), "no provider kind template") {
		t.Fatalf("profile without a kind template: %v", err)
	}
	cfg := ProviderConfig{
		Providers:                 []ProviderEntry{{ID: "ollama", ToolCallRejectionProfile: "missing"}},
		ToolCallRejectionProfiles: map[string]providerprofile.ToolCallRejectionPolicy{},
	}
	if err := resolveProviderToolCallRejectionProfiles(&cfg); err == nil || !strings.Contains(err.Error(), "unknown tool_call_rejection_profile") {
		t.Fatalf("unknown profile: %v", err)
	}
	cfg.ToolCallRejectionProfiles["bad"] = providerprofile.ToolCallRejectionPolicy{}
	if err := resolveProviderToolCallRejectionProfiles(&cfg); err == nil || !strings.Contains(err.Error(), `"bad"`) {
		t.Fatalf("invalid profile: %v", err)
	}
}
