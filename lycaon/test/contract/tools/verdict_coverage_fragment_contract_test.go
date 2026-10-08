package contract

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestVerdictCoverageFragmentMatchesWireReview keeps the coverage member a
// review phase offers in step with the CoverageReview the host decodes: the
// same members, the same required set, and the same dispositions.
func TestVerdictCoverageFragmentMatchesWireReview(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	meta, ok := cfg.ToolMeta("submit_verdict")
	if !ok {
		t.Fatal("submit_verdict schema missing")
	}
	defs, _ := meta.ArgsSchema["$defs"].(map[string]any)
	review, _ := defs["coverage_review"].(map[string]any)
	assessment := schemaChild(schemaChild(review, "assessments"), "items")

	raw, err := os.ReadFile(filepath.Join(root, "docs", "openapi", "components", "schemas", "worker.yaml"))
	contractcheck.FailErr(t, "read worker.yaml", err)
	var doc struct {
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	contractcheck.FailErr(t, "decode worker.yaml", yaml.Unmarshal(raw, &doc))
	wire := doc.Components.Schemas

	assertSameMembers(t, "coverage_review", review, wire["CoverageReview"])
	assertSameMembers(t, "coverage_review.assessments[]", assessment, wire["CoverageAssessment"])
	got := stringList(schemaChild(assessment, "disposition")["enum"])
	want := stringList(schemaChild(wire["CoverageAssessment"], "disposition")["enum"])
	if !slices.Equal(got, want) {
		t.Fatalf("dispositions = %v, wire declares %v", got, want)
	}
}

func assertSameMembers(t *testing.T, name string, fragment, wire map[string]any) {
	t.Helper()
	if fragment == nil || wire == nil {
		t.Fatalf("%s: fragment or wire schema missing", name)
	}
	if got, want := memberNames(fragment), memberNames(wire); !slices.Equal(got, want) {
		t.Fatalf("%s members = %v, wire declares %v", name, got, want)
	}
	if got, want := sortedStrings(fragment["required"]), sortedStrings(wire["required"]); !slices.Equal(got, want) {
		t.Fatalf("%s required = %v, wire declares %v", name, got, want)
	}
	if fragment["additionalProperties"] != false {
		t.Fatalf("%s must refuse undeclared members like the wire decoder", name)
	}
}

func schemaChild(schema map[string]any, name string) map[string]any {
	if name == "items" {
		child, _ := schema["items"].(map[string]any)
		return child
	}
	props, _ := schema["properties"].(map[string]any)
	child, _ := props[name].(map[string]any)
	return child
}

func memberNames(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	out := make([]string, 0, len(props))
	for name := range props {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func stringList(raw any) []string {
	values, _ := raw.([]any)
	out := make([]string, 0, len(values))
	for _, v := range values {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func sortedStrings(raw any) []string {
	out := stringList(raw)
	sort.Strings(out)
	return out
}
