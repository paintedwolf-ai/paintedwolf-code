package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

func compileSchemaBundle(t *testing.T, names ...string) *jsonschema.Schema {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "schemas")
	compiler := jsonschema.NewCompiler()
	for _, name := range names {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read schema "+name, err)
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			contractcheck.FailErr(t, "parse schema "+name, err)
		}
		// These are lycaon's own host schemas and carry a lycaon $id, so a
		// relative $ref between them resolves against that base rather than
		// against a bare filename. Registering both keeps the bundle offline.
		contractcheck.FailErr(t, "add schema "+name, compiler.AddResource(hostSchemaBase+name, doc))
		contractcheck.FailErr(t, "add schema file "+name, compiler.AddResource(name, doc))
	}
	sch, err := compiler.Compile(hostSchemaBase + names[len(names)-1])
	contractcheck.FailErr(t, "compile "+names[len(names)-1], err)
	return sch
}

// compileVendoredOARSchema compiles schemas/oar/oar.schema.json — the verbatim
// copy of the standard's rule schema. Nothing in this repository may edit it;
// ./task oar:vendor:check proves it has not been.
func compileVendoredOARSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join(contractcheck.RepoRoot(t), "schemas", "oar", "oar.schema.json")
	raw, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read vendored oar.schema.json", err)
	var doc any
	contractcheck.FailErr(t, "parse vendored oar.schema.json", json.Unmarshal(raw, &doc))
	compiler := jsonschema.NewCompiler()
	contractcheck.FailErr(t, "add vendored schema", compiler.AddResource("oar.schema.json", doc))
	sch, err := compiler.Compile("oar.schema.json")
	contractcheck.FailErr(t, "compile vendored oar.schema.json", err)
	return sch
}

// hostSchemaBase is the $id base lycaon's own host schemas carry. It is
// not the openagentrules.org base: this repository does not
// publish to that domain, and the anchor catalog and binding grammar are not
// part of the standard.
const hostSchemaBase = "https://lycaon.dev/schemas/host/"

func validateJSONInstance(t *testing.T, sch *jsonschema.Schema, raw []byte) {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	contractcheck.FailErr(t, "unmarshal instance", err)
	if err := sch.Validate(doc); err != nil {
		t.Fatalf("schema validate: %v", err)
	}
}

func TestAnchorCatalogSchemaValidatesFixturesAndCatalog(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	anchorsSch := compileSchemaBundle(t, "anchors.schema.json")
	bindingSch := compileSchemaBundle(t, "selector.schema.json", "anchor-binding.schema.json")
	// The rule schema is vendored from the standard and is self-contained: the
	// 1.0 selector is named by facts, so there is no selector schema to $ref.
	oarSch := compileVendoredOARSchema(t)

	fixtures := []struct {
		sch  *jsonschema.Schema
		path string
	}{
		{anchorsSch, "schemas/fixtures/anchors-catalog-sample.json"},
		{bindingSch, "schemas/fixtures/anchor-binding-inform-sample.json"},
		{bindingSch, "schemas/fixtures/anchor-binding-workflow-sample.json"},
		{oarSch, "schemas/fixtures/oar-sample-rule.json"},
	}
	for _, fx := range fixtures {
		raw, err := os.ReadFile(filepath.Join(root, fx.path))
		contractcheck.FailErr(t, "read "+fx.path, err)
		validateJSONInstance(t, fx.sch, raw)
	}

	catalogPath := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")
	yamlRaw, err := os.ReadFile(catalogPath)
	contractcheck.FailErr(t, "read catalog.yaml", err)
	var catalog any
	contractcheck.FailErr(t, "parse catalog.yaml", yaml.Unmarshal(yamlRaw, &catalog))
	jsonRaw, err := json.Marshal(catalog)
	contractcheck.FailErr(t, "marshal catalog", err)
	validateJSONInstance(t, anchorsSch, jsonRaw)

	var parsed struct {
		Anchors []struct {
			ID string `json:"id"`
		} `json:"anchors"`
	}
	contractcheck.FailErr(t, "unmarshal catalog ids", json.Unmarshal(jsonRaw, &parsed))
	if len(parsed.Anchors) < 40 {
		t.Fatalf("catalog anchors = %d, want >= 40 (kick+inject+OAR freeze)", len(parsed.Anchors))
	}
	seen := map[string]struct{}{}
	for _, a := range parsed.Anchors {
		if a.ID == "" {
			t.Fatal("empty anchor id")
		}
		if _, dup := seen[a.ID]; dup {
			t.Fatalf("duplicate anchor id %q", a.ID)
		}
		seen[a.ID] = struct{}{}
	}
	required := []string{
		"leg.finished", "gate.blocked", "tool.pre_invoke", "coordinator.post_turn",
		"worker.finalize", "inject.active_workflow", "inject.command_jobs", "loop.wake", "board.changed",
	}
	for _, id := range required {
		if _, ok := seen[id]; !ok {
			t.Fatalf("catalog missing required anchor %q", id)
		}
	}
}

func TestAnchorBindingInformRequiresRender(t *testing.T) {
	t.Parallel()
	sch := compileSchemaBundle(t, "selector.schema.json", "anchor-binding.schema.json")
	bad := []byte(`{"on":"leg.finished","selector":{},"effect":"inform"}`)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(bad))
	contractcheck.FailErr(t, "unmarshal", err)
	if err := sch.Validate(doc); err == nil {
		t.Fatal("expected inform Binding without render to fail")
	}
}
