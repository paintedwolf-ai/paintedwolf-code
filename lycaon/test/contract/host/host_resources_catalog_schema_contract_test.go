package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hostresources"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

func TestBundledHostResourceCatalogMatchesPublishedSchema(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schema := compileHostResourceSchema(t, filepath.Join(root, "schemas", "host-resources.schema.json"))
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "host-resources.yaml"))
	contractcheck.FailErr(t, "read bundled catalog", err)

	if err := schema.Validate(catalogInstance(t, data)); err != nil {
		t.Fatalf("bundled catalog violates schemas/host-resources.schema.json: %v", err)
	}
	if _, err := hostresources.ParseCatalog(data, "built-in"); err != nil {
		t.Fatalf("bundled catalog rejected by ParseCatalog: %v", err)
	}
}

func TestPublishedHostResourceSchemaRejectsUnknownRealizationKeys(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schema := compileHostResourceSchema(t, filepath.Join(root, "schemas", "host-resources.schema.json"))
	catalog := []byte(`version: 1
resources:
  - id: tool
    family: tools
    label: Tool
    category: Tools
    description: A tool.
    surfaces: [process_exec]
    realizations:
      - discover: {executable: {names: [tool]}}
        launch_mode: detached
`)
	if err := schema.Validate(catalogInstance(t, catalog)); err == nil {
		t.Fatal("schema accepted an unknown realization key")
	}
	if _, err := hostresources.ParseCatalog(catalog, "built-in"); err == nil {
		t.Fatal("ParseCatalog accepted an unknown realization key")
	}
}

func compileHostResourceSchema(t *testing.T, path string) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read schema", err)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	contractcheck.FailErr(t, "decode schema", err)
	compiler := jsonschema.NewCompiler()
	contractcheck.FailErr(t, "add schema", compiler.AddResource("lycaon://host-resources.schema.json", doc))
	schema, err := compiler.Compile("lycaon://host-resources.schema.json")
	contractcheck.FailErr(t, "compile schema", err)
	return schema
}

// catalogInstance converts catalog YAML to the JSON value model the validator expects.
func catalogInstance(t *testing.T, data []byte) any {
	t.Helper()
	var doc any
	contractcheck.FailErr(t, "decode catalog", yaml.Unmarshal(data, &doc))
	raw, err := json.Marshal(doc)
	contractcheck.FailErr(t, "encode catalog", err)
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	contractcheck.FailErr(t, "decode catalog json", err)
	return inst
}

func TestBundledHostResourceUsesNameCatalogResources(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "host", "host-resources.yaml"))
	contractcheck.FailErr(t, "read bundled catalog", err)
	catalog, err := hostresources.ParseCatalog(data, "built-in")
	contractcheck.FailErr(t, "parse bundled catalog", err)
	ids := map[string]bool{}
	for _, def := range catalog.Resources {
		ids[def.ID] = true
	}
	for _, def := range catalog.Resources {
		for _, realization := range def.Realizations {
			for _, use := range realization.Uses {
				for _, id := range use.Requires.AnyResource {
					if !ids[id] || id == def.ID {
						t.Errorf("%s use %s runs through %q, which is not another catalog resource", def.ID, use.ID, id)
					}
				}
			}
		}
	}
}
