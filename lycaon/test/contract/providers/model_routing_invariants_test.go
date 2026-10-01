package contract

import (
	"os"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestBundledModelPolicyHasCoordinatorLiteAndPool(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "model-policy.yaml")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		contractcheck.FailErr(t, "unmarshal YAML document", err)
	}
	for _, key := range []string{"coordinator", "lite", "agent_pool"} {
		if doc[key] == nil {
			t.Fatalf("model-policy.yaml missing %q", key)
		}
	}
	pool, ok := doc["agent_pool"].(map[string]any)
	if !ok {
		t.Fatal("agent_pool must be an object")
	}
	models, ok := pool["models"].([]any)
	if !ok {
		t.Fatal("agent_pool.models must be a list (may be empty when unset)")
	}
	_ = models
}
