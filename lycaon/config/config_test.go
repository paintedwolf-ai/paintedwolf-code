package config

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

// TestEmbeddedCatalogPresent verifies embedded pack availability.
func TestEmbeddedCatalogPresent(t *testing.T) {
	for _, rel := range []Rel{
		StockPacks + "/plan/workflows/plan/workflow.yaml",
		StockPacks + "/platform/host/surface-profiles.yaml",
		ScannersDir + "/scanners.yaml",
		StockPacks + "/platform/" + PackManifestName,
	} {
		if !Has(rel) {
			t.Fatalf("embedded config missing %q", rel)
		}
	}
	if !Has(StockPacks + "/platform/workflows/_topologies") {
		t.Fatal("embedded config missing _-prefixed dir (all: pattern?)")
	}
}

func TestRelNormalizes(t *testing.T) {
	cases := map[Rel]string{
		"packs/painted-wolf/plan/workflow.yaml": "packs/painted-wolf/plan/workflow.yaml",
		"/packs/x.yaml":                         "packs/x.yaml",
		"  packs/y.yaml  ":                      "packs/y.yaml",
		"packs/./z.yaml":                        "packs/z.yaml",
		"":                                      ".",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("Rel(%q).String() = %q, want %q", string(in), got, want)
		}
	}
	if got := StockPacks.Join("plan", "workflows"); got != "packs/painted-wolf/plan/workflows" {
		t.Errorf("Join = %q", got)
	}
	if got := Providers.Base(); got != "providers.yaml" {
		t.Errorf("Base = %q", got)
	}
}

// Bundled reads do not depend on the working directory.
func TestReadResolvesWithoutHostTree(t *testing.T) {
	t.Chdir(t.TempDir())
	b, err := Read(Providers)
	if err != nil {
		t.Fatalf("read providers from a directory with no config tree: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("empty providers.yaml")
	}
}

// Walk returns paths relative to the walk root.
func TestWalkReportsRootedRelPaths(t *testing.T) {
	root := StockPacks.Join("plan", "workflows")
	var found []Rel
	err := Walk(root, func(rel Rel, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	want := root.Join("plan", WorkflowManifestName)
	for _, p := range found {
		if p == want {
			return
		}
	}
	t.Fatalf("walk did not report %s; got %v", want, found)
}

// Partial overrides inherit unspecified embedded files.
func TestOverrideLayersOverEmbed(t *testing.T) {
	partial := fstest.MapFS{Providers.String(): &fstest.MapFile{Data: []byte("overridden: true\n")}}
	restore := UseFS(overlay{disk: partial, base: embedded})
	defer restore()

	got, err := Read(Providers)
	if err != nil {
		t.Fatalf("read overridden file: %v", err)
	}
	if string(got) != "overridden: true\n" {
		t.Fatalf("override not served, got %q", got)
	}
	if !Has(ModelPolicy) {
		t.Fatal("unshadowed default disappeared under a partial override")
	}
}

func TestDecodeYAMLRejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	var target struct {
		Name string `yaml:"name"`
	}
	if err := DecodeYAML([]byte("name: ok\nstale: true\n"), &target); err == nil {
		t.Fatal("DecodeYAML accepted an unknown field")
	}
	if err := DecodeYAML([]byte("name: ok\n---\nname: second\n"), &target); err == nil {
		t.Fatal("DecodeYAML accepted multiple documents")
	}
}
