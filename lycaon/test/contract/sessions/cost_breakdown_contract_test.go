package contract

import (
	"os"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestCostSummaryOpenAPIRequiresBreakdown(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "docs", "openapi.yaml"))
	contractcheck.FailErr(t, "read file", err)
	var doc struct {
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		contractcheck.FailErr(t, "unmarshal YAML document", err)
	}
	schema, ok := doc.Components.Schemas["CostSummary"]
	if !ok {
		t.Fatal("CostSummary schema missing")
	}
	required := schemaRequiredList(schema)
	for _, field := range []string{"coordinator", "workers", "summarizer", "pricing_provenance"} {
		if !stringInList(required, field) {
			t.Fatalf("CostSummary required = %v missing %q", required, field)
		}
	}
}

func schemaRequiredList(schema map[string]any) []string {
	raw, ok := schema["required"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringInList(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
