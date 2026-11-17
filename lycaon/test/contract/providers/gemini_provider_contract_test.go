package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Bundled providers are templates with discovered models.
func TestBundledProvidersIncludeGemini(t *testing.T) {
	t.Parallel()
	cfg, err := llm.LoadProviderConfig()
	contractcheck.FailErr(t, "llm.LoadProviderConfig failed", err)
	var gemini *llm.ProviderEntry
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "gemini" {
			gemini = &cfg.Providers[i]
			break
		}
	}
	if gemini == nil {
		t.Fatal("bundled providers missing gemini")
	}
	wantBase := "https://generativelanguage.googleapis.com/v1beta/openai"
	if gemini.BaseURL != wantBase {
		t.Fatalf("base_url = %q, want %q", gemini.BaseURL, wantBase)
	}
	if gemini.APIKeyEnv != "GEMINI_API_KEY" {
		t.Fatalf("api_key_env = %q", gemini.APIKeyEnv)
	}
	if gemini.Kind != "gemini" {
		t.Fatalf("kind = %q, want gemini", gemini.Kind)
	}
	if len(gemini.Models) != 0 {
		t.Fatalf("ship gemini models = %d, want 0 (live discovery only)", len(gemini.Models))
	}
}
