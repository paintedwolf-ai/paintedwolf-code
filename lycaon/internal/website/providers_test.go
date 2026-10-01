package website

import (
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMergeProviders(t *testing.T) {
	catalog := &llm.ProviderConfig{
		Providers: []llm.ProviderEntry{
			{ID: "openai", Label: "OpenAI", BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY"},
			{ID: "openai-compatible", Kind: "openai-compatible", Label: "OpenAI-compatible", BaseURL: "http://127.0.0.1:8080/v1", APIKeyEnv: ""},
			{ID: "ollama", Label: "Ollama", BaseURL: "http://localhost:11434/v1", APIKeyEnv: ""},
		},
	}
	overlay := &ProvidersOverlay{
		Groups: []Group{
			{ID: "local", Label: "Run locally"},
			{ID: "cloud", Label: "Cloud / hosted"},
		},
		Providers: map[string]OverlayProvider{
			"openai":            {KeyURL: "https://platform.openai.com/api-keys"},
			"openai-compatible": {Group: "custom"},
			"ollama":            {DownloadURL: "https://ollama.com/download", Pull: "ollama pull llama3.1"},
		},
		ExtraProviders: []WebsiteProvider{
			{ID: "custom", Name: "Other host", Group: "custom", Custom: true},
		},
	}

	out, err := MergeProviders(catalog, overlay)
	if err != nil {
		t.Fatalf("MergeProviders: %v", err)
	}
	if len(out.Providers) != 4 {
		t.Fatalf("providers len = %d, want 4", len(out.Providers))
	}
	if out.Providers[0].ID != "ollama" || out.Providers[0].Group != "local" {
		t.Fatalf("first provider = %+v, want ollama local", out.Providers[0])
	}
	if out.Providers[1].ID != "openai" || out.Providers[1].Group != "cloud" {
		t.Fatalf("second provider = %+v, want openai cloud", out.Providers[1])
	}
	if out.Providers[2].ID != "openai-compatible" || out.Providers[2].Group != "custom" {
		t.Fatalf("third provider = %+v, want openai-compatible custom", out.Providers[2])
	}
	if !out.Providers[3].Custom {
		t.Fatalf("third provider should be custom")
	}
}

func TestMergeProvidersProjectsPlatforms(t *testing.T) {
	catalog := &llm.ProviderConfig{
		Providers: []llm.ProviderEntry{
			{ID: "omlx", Label: "oMLX", APIKeyEnv: "", Platforms: []string{"macos"}},
			{ID: "ollama", Label: "Ollama", APIKeyEnv: ""},
		},
	}
	overlay := &ProvidersOverlay{
		Groups: []Group{{ID: "local", Label: "Run locally"}},
		Providers: map[string]OverlayProvider{
			"omlx":   {DownloadURL: "https://omlx.ai"},
			"ollama": {DownloadURL: "https://ollama.com/download"},
		},
	}

	out, err := MergeProviders(catalog, overlay)
	if err != nil {
		t.Fatalf("MergeProviders: %v", err)
	}
	got := map[string]WebsiteProvider{}
	for _, p := range out.Providers {
		got[p.ID] = p
	}
	if got["omlx"].Platforms == nil || len(got["omlx"].Platforms) != 1 || got["omlx"].Platforms[0] != "macos" {
		t.Fatalf("omlx platforms = %v, want [macos]", got["omlx"].Platforms)
	}
	if len(got["ollama"].Platforms) != 0 {
		t.Fatalf("ollama platforms = %v, want empty (every host)", got["ollama"].Platforms)
	}
}

func TestMergeProvidersProjectsAuthShape(t *testing.T) {
	yes := true
	catalog := &llm.ProviderConfig{
		Providers: []llm.ProviderEntry{
			// Key-only: a stored key is the sole way in.
			{ID: "openai", Label: "OpenAI", APIKeyEnv: "OPENAI_API_KEY"},
			// Either mode: bearer token or the AWS credential chain.
			{ID: "bedrock", Label: "Amazon Bedrock", APIKeyEnv: "AWS_BEARER_TOKEN_BEDROCK", AmbientAuth: "aws-sdk-chain"},
			// Ambient only: no pasteable credential exists for this kind.
			{ID: "vertex", Label: "Google Vertex AI", APIKeyEnv: "", AmbientAuth: "google-adc"},
			// Keyless local.
			{ID: "ollama", Label: "Ollama", APIKeyEnv: ""},
			// Explicit override wins over the api_key_env derivation.
			{ID: "forced", Label: "Forced", APIKeyEnv: "", RequiresAPIKey: &yes},
		},
	}
	overlay := &ProvidersOverlay{
		Groups: []Group{{ID: "local", Label: "Run locally"}, {ID: "cloud", Label: "Cloud"}},
		Providers: map[string]OverlayProvider{
			"openai":  {KeyURL: "https://platform.openai.com/api-keys"},
			"bedrock": {},
			"vertex":  {},
			"ollama":  {DownloadURL: "https://ollama.com/download"},
			"forced":  {},
		},
	}

	out, err := MergeProviders(catalog, overlay)
	if err != nil {
		t.Fatalf("MergeProviders: %v", err)
	}
	got := map[string]WebsiteProvider{}
	for _, p := range out.Providers {
		got[p.ID] = p
	}
	for _, tc := range []struct {
		id      string
		wantKey bool
		wantAmb string
	}{
		{"openai", true, ""},
		{"bedrock", true, "aws-sdk-chain"},
		{"vertex", false, "google-adc"},
		{"ollama", false, ""},
		{"forced", true, ""},
	} {
		p, ok := got[tc.id]
		if !ok {
			t.Fatalf("provider %q missing from output", tc.id)
		}
		if p.RequiresAPIKey != tc.wantKey || p.AmbientAuth != tc.wantAmb {
			t.Errorf("%s: requires_api_key=%v ambient_auth=%q, want %v / %q",
				tc.id, p.RequiresAPIKey, p.AmbientAuth, tc.wantKey, tc.wantAmb)
		}
	}
}

func TestMergeProvidersRejectsKeyURLWithoutKeyField(t *testing.T) {
	catalog := &llm.ProviderConfig{
		Providers: []llm.ProviderEntry{
			{ID: "vertex", Label: "Google Vertex AI", APIKeyEnv: "", AmbientAuth: "google-adc"},
		},
	}
	overlay := &ProvidersOverlay{
		Groups: []Group{{ID: "cloud", Label: "Cloud"}},
		Providers: map[string]OverlayProvider{
			"vertex": {KeyURL: "https://console.cloud.google.com/vertex-ai"},
		},
	}
	_, err := MergeProviders(catalog, overlay)
	if err == nil || !strings.Contains(err.Error(), "no key field") {
		t.Fatalf("expected key_url rejection for an ambient-only provider, got %v", err)
	}
}

func TestMergeProvidersMissingOverlay(t *testing.T) {
	catalog := &llm.ProviderConfig{
		Providers: []llm.ProviderEntry{{ID: "gemini", Label: "Gemini", APIKeyEnv: "GEMINI_API_KEY"}},
	}
	overlay := &ProvidersOverlay{
		Groups:    []Group{{ID: "cloud", Label: "Cloud"}},
		Providers: map[string]OverlayProvider{},
	}
	if _, err := MergeProviders(catalog, overlay); err == nil || !strings.Contains(err.Error(), "overlay missing") {
		t.Fatalf("expected overlay missing error, got %v", err)
	}
}

func TestRenderProvidersYAMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	overlayPath := filepath.Join(dir, "overlay.yaml")

	// The engine catalog ships inside the binary, so a fixture on disk cannot stand in
	// for it. Stage the bundled file instead, or the round-trip asserts against whatever
	// providers happen to be shipped.
	configtest.Overlay(t, map[config.Rel]string{config.Providers: `providers:
  - id: fireworks
    label: Fireworks
    base_url: https://api.fireworks.ai/inference/v1
    api_key_env: FIREWORKS_API_KEY
    models: []
`})
	overlayYAML := `groups:
  - id: cloud
    label: Cloud / hosted
providers:
  fireworks:
    key_url: https://fireworks.ai/account/api-keys
`
	if err := os.WriteFile(overlayPath, []byte(overlayYAML), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	body, err := RenderProvidersYAML(overlayPath)
	if err != nil {
		t.Fatalf("RenderProvidersYAML: %v", err)
	}
	if !strings.HasPrefix(string(body), "# Codegened from paintedwolf-ai/lycaon") {
		t.Fatalf("missing generated header")
	}
	if !strings.Contains(string(body), "id: fireworks") {
		t.Fatalf("missing fireworks entry: %s", body)
	}
}
