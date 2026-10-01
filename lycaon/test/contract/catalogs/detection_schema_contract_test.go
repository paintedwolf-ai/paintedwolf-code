package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/detectionpack"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

func TestDetectionSchemasValidateBundledCatalog(t *testing.T) {
	t.Parallel()
	ruleSchema := compileSchemaBundle(t, "detection-rule.schema.json")
	packSchema := compileSchemaBundle(t, "detection-pack.schema.json")
	root := filepath.Join(
		contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "security", "host", "detection-packs",
	)
	packDirs, err := filepath.Glob(filepath.Join(root, "*"))
	contractcheck.FailErr(t, "glob detection packs", err)
	if len(packDirs) == 0 {
		t.Fatal("bundled detection catalog is empty")
	}
	for _, packDir := range packDirs {
		info, statErr := os.Stat(packDir)
		contractcheck.FailErr(t, "stat detection pack", statErr)
		if !info.IsDir() {
			continue
		}
		validateYAMLWithSchema(t, packSchema, filepath.Join(packDir, "pack.yaml"))
		rules, globErr := filepath.Glob(filepath.Join(packDir, "rules", "*.yml"))
		contractcheck.FailErr(t, "glob detection rules", globErr)
		for _, rule := range rules {
			validateYAMLWithSchema(t, ruleSchema, rule)
		}
	}
}

func TestDetectionRuleSchemaFieldProjectionMatchesRuntime(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), "schemas", "detection-rule.schema.json"))
	contractcheck.FailErr(t, "read detection rule schema", err)
	var doc struct {
		Fields map[string][]string `json:"x-paintedwolf-fields-by-service"`
	}
	contractcheck.FailErr(t, "parse detection rule schema", json.Unmarshal(raw, &doc))
	want := map[string][]string{
		string(detectionpack.SourceToolExec):       detectionpack.SupportedFields(detectionpack.SourceToolExec),
		string(detectionpack.SourceEgressObserved): detectionpack.SupportedFields(detectionpack.SourceEgressObserved),
	}
	for source, fields := range doc.Fields {
		sort.Strings(fields)
		doc.Fields[source] = fields
	}
	if !reflect.DeepEqual(doc.Fields, want) {
		t.Fatalf("schema fields=%v runtime=%v", doc.Fields, want)
	}
}

func TestDetectionRuleSchemaRejectsLoaderInvalidShapes(t *testing.T) {
	t.Parallel()
	sch := compileSchemaBundle(t, "detection-rule.schema.json")
	for name, instance := range map[string]map[string]any{
		"missing description":  detectionRuleInstance(),
		"non-string selection": detectionRuleInstanceWithDescription(map[string]any{"Contained": true}),
		"invalid status": func() map[string]any {
			v := detectionRuleInstanceWithDescription(map[string]any{"Tool": "command"})
			v["status"] = "retired"
			return v
		}(),
	} {
		raw, err := json.Marshal(instance)
		contractcheck.FailErr(t, "marshal "+name, err)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		contractcheck.FailErr(t, "decode "+name, err)
		if err := sch.Validate(doc); err == nil {
			t.Fatalf("%s unexpectedly validated", name)
		}
	}
}

func detectionRuleInstance() map[string]any {
	return map[string]any{
		"title": "Test",
		"id":    "58cc1c3e-07ec-49fc-a201-7f3f5ab89aa2",
		"logsource": map[string]any{
			"product": "lycaon",
			"service": "tool_exec",
		},
		"level": "high",
		"detection": map[string]any{
			"selection": map[string]any{"Tool": "command"},
			"condition": "selection",
		},
	}
}

func detectionRuleInstanceWithDescription(selection map[string]any) map[string]any {
	v := detectionRuleInstance()
	v["description"] = "A test rule."
	v["detection"] = map[string]any{"selection": selection, "condition": "selection"}
	return v
}

func validateYAMLWithSchema(t *testing.T, sch *jsonschema.Schema, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read "+path, err)
	var instance any
	contractcheck.FailErr(t, "parse "+path, yaml.Unmarshal(raw, &instance))
	encoded, err := json.Marshal(instance)
	contractcheck.FailErr(t, "encode "+path, err)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	contractcheck.FailErr(t, "decode "+path, err)
	if err := sch.Validate(doc); err != nil {
		t.Fatalf("%s: schema validation: %v", path, err)
	}
}
