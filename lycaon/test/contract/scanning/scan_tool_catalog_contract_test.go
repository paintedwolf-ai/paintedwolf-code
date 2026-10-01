package contract

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// Two scan argument enums restate a generated wire vocabulary. Drift there is
// silent: the tool keeps working and the model is told a value set the host no
// longer accepts.

const scanSchemaDir = "lycaon/config/packs/painted-wolf/platform/tools/schemas"

// catalogOwnedScanTools are the scan tools whose description and args schema the
// catalog defines.
var catalogOwnedScanTools = []string{
	"scan_pack", "scan_list", "scan_summary", "scan_query", "scan_compare",
}

func TestScanToolsAreCatalogOwned(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, name := range catalogOwnedScanTools {
		entry := readScanSchemaEntry(t, root, name)
		if strings.TrimSpace(entry.Description) == "" {
			t.Errorf("%s.yaml carries no description — the model would get none", name)
		}
		if entry.Schema == nil {
			t.Errorf("%s.yaml carries no schema", name)
		}
	}
}

func TestScanToolCatalogEnumsMatchWire(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	wantCategories := []string{"all"}
	for _, c := range api.AllScanCategoryValues() {
		wantCategories = append(wantCategories, string(c))
	}
	got := schemaEnumAt(t, readScanSchemaEntry(t, root, "scan_pack").Schema,
		"properties", "categories", "items", "enum")
	assertSameSet(t, "scan_pack.categories", got, wantCategories)

	var wantLevels []string
	for _, l := range api.AllFindingLevelValues() {
		wantLevels = append(wantLevels, string(l))
	}
	got = schemaEnumAt(t, readScanSchemaEntry(t, root, "scan_query").Schema,
		"properties", "level", "enum")
	assertSameSet(t, "scan_query.level", got, wantLevels)
}

func readScanSchemaEntry(t *testing.T, root, tool string) toolschema.Entry {
	t.Helper()
	rel := filepath.Join(scanSchemaDir, tool+".yaml")
	var entry toolschema.Entry
	if err := yaml.Unmarshal([]byte(contractcheck.ReadRepoFile(t, root, filepath.ToSlash(rel))), &entry); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return entry
}

// schemaEnumAt walks a decoded schema to an enum array and returns its strings.
func schemaEnumAt(t *testing.T, schema map[string]any, path ...string) []string {
	t.Helper()
	cur := any(schema)
	for i, key := range path {
		node, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("schema path %s: %q is not a mapping", strings.Join(path, "."), path[i-1])
		}
		cur, ok = node[key]
		if !ok {
			t.Fatalf("schema path %s: %q is missing", strings.Join(path, "."), key)
		}
	}
	raw, ok := cur.([]any)
	if !ok {
		t.Fatalf("schema path %s is not a list", strings.Join(path, "."))
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("schema path %s holds a non-string value %v", strings.Join(path, "."), v)
		}
		out = append(out, s)
	}
	return out
}

func assertSameSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if strings.Join(g, ",") != strings.Join(w, ",") {
		t.Errorf("%s enum drifted from the generated wire vocabulary.\n  catalog: %s\n  wire:    %s\n"+
			"The model is told the catalog value set; regenerate or edit the schema file to match.",
			label, strings.Join(g, ","), strings.Join(w, ","))
	}
}
