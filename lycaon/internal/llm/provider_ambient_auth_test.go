package llm

import (
	"os"
	"path/filepath"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/testutil"
)

// ambientShipYAML models the two ambient-capable shapes: a kind that takes a pasted
// key with the ambient chain as the alternative (bedrock), and a kind that can
// only authenticate ambiently (vertex).
const ambientShipYAML = `providers:
  - id: keyfirst
    kind: keyfirst
    label: Key First
    base_url: us-east-1
    endpoint_style: region
    api_key_env: AWS_BEARER_TOKEN_BEDROCK
    ambient_auth: aws-sdk-chain
    models: []
    http_retry:
      max_retries: 0
      max_wait_ms: 1000
      backoff_ms: []
      statuses: [429]
      wait_headers: [Retry-After]
  - id: ambientonly
    kind: ambientonly
    label: Ambient Only
    base_url: ""
    endpoint_style: derived
    api_key_env: ""
    ambient_auth: google-adc
    models: []
    http_retry:
      max_retries: 0
      max_wait_ms: 1000
      backoff_ms: []
      statuses: [429]
      wait_headers: [Retry-After]
`

func ambientCatalog(t *testing.T, localYAML string) *ProviderCatalog {
	t.Helper()
	stageShipProviders(t, ambientShipYAML)
	local := filepath.Join(t.TempDir(), "providers.local.yaml")
	testutil.FailErr(t, "write local providers", os.WriteFile(local, []byte(localYAML), 0o600))
	catalog, err := NewProviderCatalogAt(local)
	testutil.FailErr(t, "NewProviderCatalogAt", err)
	return catalog
}

// A kind naming an ambient chain still defaults to a stored key when it has a
// key env — the ambient chain is the opt-in alternative, not the default.
func TestKindTemplateAmbientAuthKeepsKeyDefault(t *testing.T) {
	catalog := ambientCatalog(t, "providers: []\n")
	byKind := make(map[string]ProviderKindTemplate)
	for _, tmpl := range catalog.KindTemplates() {
		byKind[tmpl.Kind] = tmpl
	}

	keyFirst, ok := byKind["keyfirst"]
	if !ok {
		t.Fatal("keyfirst template missing")
	}
	if !keyFirst.RequiresAPIKey {
		t.Error("requires_api_key = false, want a stored key as the default mode")
	}
	if keyFirst.AmbientAuth != "aws-sdk-chain" {
		t.Errorf("ambient_auth = %q, want aws-sdk-chain", keyFirst.AmbientAuth)
	}
	if keyFirst.EndpointStyle != EndpointStyleRegion {
		t.Errorf("endpoint_style = %q, want region", keyFirst.EndpointStyle)
	}

	ambientOnly, ok := byKind["ambientonly"]
	if !ok {
		t.Fatal("ambientonly template missing")
	}
	if ambientOnly.RequiresAPIKey {
		t.Error("requires_api_key = true, want ambient-only (no pasteable credential)")
	}
	if ambientOnly.AmbientAuth != "google-adc" {
		t.Errorf("ambient_auth = %q, want google-adc", ambientOnly.AmbientAuth)
	}
	if ambientOnly.EndpointStyle != EndpointStyleDerived {
		t.Errorf("endpoint_style = %q, want derived", ambientOnly.EndpointStyle)
	}
}

func TestLocalInstanceWithoutRecordedModeInheritsKindDefault(t *testing.T) {
	catalog := ambientCatalog(t, `providers:
  - id: keyfirst
    kind: keyfirst
    base_url: us-east-1
    api_key_env: AWS_ACCESS_KEY_ID
    models: []
`)
	entry, ok := catalog.Get("keyfirst")
	if !ok {
		t.Fatal("keyfirst instance missing")
	}
	if !entry.RequiresAPIKey {
		t.Error("requires_api_key = false, want the ship kind default")
	}
	if entry.AmbientAuth != "aws-sdk-chain" {
		t.Errorf("ambient_auth = %q, want it inherited from the ship kind", entry.AmbientAuth)
	}
}

// An explicit recorded mode always wins, in both directions.
func TestLocalInstanceRecordedModeWins(t *testing.T) {
	catalog := ambientCatalog(t, `providers:
  - id: keyed
    kind: keyfirst
    base_url: us-east-1
    api_key_env: AWS_BEARER_TOKEN_BEDROCK
    requires_api_key: true
    models: []
  - id: ambient
    kind: keyfirst
    base_url: eu-west-1
    api_key_env: AWS_BEARER_TOKEN_BEDROCK
    requires_api_key: false
    models: []
`)
	keyed, ok := catalog.Get("keyed")
	if !ok {
		t.Fatal("keyed instance missing")
	}
	if !keyed.RequiresAPIKey {
		t.Error("keyed instance: requires_api_key = false, want the recorded key mode")
	}
	ambient, ok := catalog.Get("ambient")
	if !ok {
		t.Fatal("ambient instance missing")
	}
	if ambient.RequiresAPIKey {
		t.Error("ambient instance: requires_api_key = true, want the recorded ambient mode")
	}
	if ambient.AmbientAuth != "aws-sdk-chain" {
		t.Errorf("ambient_auth = %q, want it inherited from the ship kind", ambient.AmbientAuth)
	}
}

// Ambient mode reports configured without a stored key; key mode does not.
func TestRegistryConfiguredFollowsCredentialMode(t *testing.T) {
	catalog := ambientCatalog(t, `providers:
  - id: keyed
    kind: keyfirst
    base_url: us-east-1
    api_key_env: AWS_BEARER_TOKEN_BEDROCK
    requires_api_key: true
    models: []
  - id: ambient
    kind: keyfirst
    base_url: eu-west-1
    api_key_env: AWS_BEARER_TOKEN_BEDROCK
    requires_api_key: false
    models: []
`)
	creds := providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age"))
	registry, err := NewRegistry(catalog, creds)
	testutil.FailErr(t, "NewRegistry", err)

	if registry.IsConfigured("keyed") {
		t.Error("key-mode instance reported configured with no stored key")
	}
	if !registry.IsConfigured("ambient") {
		t.Error("ambient-mode instance reported unconfigured; the chain is the credential")
	}
	testutil.FailErr(t, "store key", creds.Set("keyed", "bedrock-api-key"))
	testutil.FailErr(t, "rebuild provider snapshot", registry.Reload(t.Context()))
	if !registry.IsConfigured("keyed") {
		t.Error("key-mode instance still unconfigured after storing a key")
	}
}
