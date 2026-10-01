package db_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRemoveStorePreservesProviderConfiguration(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)

	catalog, err := llm.NewProviderCatalog()
	testutil.FailErr(t, "open provider catalog", err)
	requiresKey := true
	testutil.FailErr(t, "save provider", catalog.Put(llm.ProviderEntry{
		ID:             "launch-provider",
		Kind:           "openai",
		Label:          "Launch Provider",
		BaseURL:        "https://example.com/v1",
		RequiresAPIKey: &requiresKey,
		Models:         []modelinfo.Entry{{ID: "launch-model"}},
	}))

	credentials, err := providercredentials.New()
	testutil.FailErr(t, "open credential store", err)
	testutil.FailErr(t, "save provider credential", credentials.Set("launch-provider", "launch-secret"))

	policy, err := llm.NewPolicyStore()
	testutil.FailErr(t, "open model policy", err)
	testutil.FailErr(t, "save model policy", policy.PutGlobal(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "launch-provider", Model: "launch-model"},
		Lite:        llm.ModelRef{ProviderID: "lite-provider", Model: "lite-model"},
		AgentPool: llm.AgentPool{
			Selection: llm.PoolSelectionFirst,
			Models:    []llm.ModelRef{{ProviderID: "launch-provider", Model: "launch-model"}},
		},
	}))

	providersPath := filepath.Join(configDir, "providers.local.yaml")
	policyPath := filepath.Join(configDir, "model-policy.yaml")
	beforeProviders, err := os.ReadFile(providersPath)
	testutil.FailErr(t, "read providers before wipe", err)
	beforePolicy, err := os.ReadFile(policyPath)
	testutil.FailErr(t, "read model policy before wipe", err)

	dbPath := filepath.Join(configDir, "store.db")
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		testutil.FailErr(t, "write database artifact", os.WriteFile(path, []byte("runtime"), 0o600))
	}
	testutil.FailErr(t, "remove store", db.RemoveStore(dbPath))

	afterProviders, err := os.ReadFile(providersPath)
	testutil.FailErr(t, "read providers after wipe", err)
	if string(afterProviders) != string(beforeProviders) {
		t.Fatalf("providers.local.yaml changed by store wipe")
	}
	afterPolicy, err := os.ReadFile(policyPath)
	testutil.FailErr(t, "read model policy after wipe", err)
	if string(afterPolicy) != string(beforePolicy) {
		t.Fatalf("model-policy.yaml changed by store wipe")
	}

	reloadedCatalog, err := llm.NewProviderCatalog()
	testutil.FailErr(t, "reload provider catalog", err)
	provider, ok := reloadedCatalog.Get("launch-provider")
	if !ok || provider.Label != "Launch Provider" {
		t.Fatalf("provider after database wipe = %+v, found=%t", provider, ok)
	}
	reloadedCredentials, err := providercredentials.New()
	testutil.FailErr(t, "reload credential store", err)
	if key, ok := reloadedCredentials.Get("launch-provider"); !ok || key != "launch-secret" {
		t.Fatalf("credential after database wipe = %q, found=%t", key, ok)
	}
	reloadedPolicy, err := llm.NewPolicyStore()
	testutil.FailErr(t, "reload model policy", err)
	reloaded, err := reloadedPolicy.Overlay(llm.SettingsScopeGlobal, "")
	testutil.FailErr(t, "get reloaded model policy", err)
	if reloaded.Coordinator.ProviderID != "launch-provider" || reloaded.Coordinator.Model != "launch-model" {
		t.Fatalf("coordinator after database wipe = %+v", reloaded.Coordinator)
	}
	if reloaded.Lite.ProviderID != "lite-provider" || reloaded.Lite.Model != "lite-model" {
		t.Fatalf("lite after database wipe = %+v", reloaded.Lite)
	}
}
