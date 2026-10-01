package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"gopkg.in/yaml.v3"
)

func TestCostBreakdownDTOFieldSync(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	specs := []wirespec.DtoSyncSpec{
		{GoValue: api.TokenTotals{}, OpenAPISchema: "TokenTotals", TSInterface: "TokenTotals"},
		{GoValue: api.CacheSavings{}, OpenAPISchema: "CacheSavings", TSInterface: "CacheSavings"},
		{GoValue: api.CostBreakdown{}, OpenAPISchema: "CostBreakdown", TSInterface: "CostBreakdown"},
		{GoValue: api.CostPricingProvenance{}, OpenAPISchema: "CostPricingProvenance", TSInterface: "CostPricingProvenance"},
		{GoValue: api.CostSummary{}, OpenAPISchema: "CostSummary", TSInterface: "CostSummary"},
		{GoValue: api.ProjectCostReport{}, OpenAPISchema: "ProjectCostReport", TSInterface: "ProjectCostReport"},
		{GoValue: api.CostEvent{}, OpenAPISchema: "CostEvent", TSInterface: "CostEvent"},
	}
	for _, spec := range specs {
		t.Run(spec.OpenAPISchema, func(t *testing.T) {
			t.Parallel()
			if err := wirespec.SyncDTOFields(root, spec); err != nil {
				contractcheck.FailErr(t, "sync wire DTO fields across Go, OpenAPI, and TS", err)
			}
		})
	}
}

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
