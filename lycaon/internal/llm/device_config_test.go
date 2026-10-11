package llm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDeviceConfigFilesUnchangedByOpen(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: emptyModelPolicyYAML})

	localYAML := `providers:
  - id: keep-me
    kind: ollama
    base_url: http://localhost:11434/v1
    models:
      - id: gemma
`
	policyYAML := `coordinator:
  provider_id: fireworks-1
  model: glm
lite:
  provider_id: ollama-1
  model: gemma4
agent_pool:
  selection: first
  models:
    - provider_id: fireworks-1
      model: glm
`
	localPath := filepath.Join(configDir, "providers.local.yaml")
	policyPath := filepath.Join(configDir, "model-policy.yaml")
	testutil.FailErr(t, "write providers", os.WriteFile(localPath, []byte(localYAML), 0o600))
	testutil.FailErr(t, "write policy", os.WriteFile(policyPath, []byte(policyYAML), 0o600))
	beforeLocal, err := os.ReadFile(localPath)
	testutil.FailErr(t, "read providers", err)
	beforePolicy, err := os.ReadFile(policyPath)
	testutil.FailErr(t, "read policy", err)

	catalog, err := NewProviderCatalog()
	testutil.FailErr(t, "open catalog", err)
	_, err = NewPolicyStore()
	testutil.FailErr(t, "open policy", err)
	_, err = NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(configDir, "credential-vault.age")))
	testutil.FailErr(t, "open registry", err)

	afterLocal, err := os.ReadFile(localPath)
	testutil.FailErr(t, "reread providers", err)
	if string(afterLocal) != string(beforeLocal) {
		t.Fatalf("opening catalog/registry rewrote providers.local.yaml")
	}
	afterPolicy, err := os.ReadFile(policyPath)
	testutil.FailErr(t, "reread policy", err)
	if string(afterPolicy) != string(beforePolicy) {
		t.Fatalf("opening policy store rewrote model-policy.yaml")
	}
}
