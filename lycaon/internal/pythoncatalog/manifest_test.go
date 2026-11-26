package pythoncatalog

import (
	"strings"
	"testing"
)

func TestDefaultLaunchManifestRoundTripAndRoster(t *testing.T) {
	t.Parallel()
	m := DefaultLaunchManifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("DefaultLaunchManifest.Validate: %v", err)
	}
	if len(m.PublicDistributions()) != LaunchPublicDistributionCount {
		t.Fatalf("public distributions = %d, want %d", len(m.PublicDistributions()), LaunchPublicDistributionCount)
	}
	if len(m.StdlibCapabilities) != 5 {
		t.Fatalf("stdlib capability groups = %d, want 5", len(m.StdlibCapabilities))
	}

	raw, err := MarshalImageManifest(m)
	if err != nil {
		t.Fatalf("MarshalImageManifest: %v", err)
	}
	parsed, err := ParseImageManifest(raw)
	if err != nil {
		t.Fatalf("ParseImageManifest: %v", err)
	}
	if parsed.SchemaVersion != ImageManifestSchemaVersion {
		t.Fatalf("schema_version = %q, want %q", parsed.SchemaVersion, ImageManifestSchemaVersion)
	}
	if len(parsed.Distributions) != LaunchPublicDistributionCount {
		t.Fatalf("parsed distributions = %d, want %d", len(parsed.Distributions), LaunchPublicDistributionCount)
	}

	wantPublic := map[string]string{
		"PyYAML":         "yaml",
		"openpyxl":       "openpyxl",
		"pytest":         "pytest",
		"tomlkit":        "tomlkit",
		"ruamel.yaml":    "ruamel.yaml",
		"packaging":      "packaging",
		"jsonschema":     "jsonschema",
		"asttokens":      "asttokens",
		"Jinja2":         "jinja2",
		"beautifulsoup4": "bs4",
		"markdown-it-py": "markdown_it",
		"requests":       "requests",
		"pypdf":          "pypdf",
	}
	for distName, wantImport := range wantPublic {
		dist, ok := parsed.FindDistribution(distName)
		if !ok {
			t.Fatalf("missing launch distribution %q", distName)
		}
		byImp, ok := parsed.FindByImport(wantImport)
		if !ok || byImp.NormalizedName != dist.NormalizedName {
			t.Fatalf("FindByImport(%q) = (%q, %v), want %q", wantImport, byImp.NormalizedName, ok, dist.NormalizedName)
		}
	}

	scripts := parsed.AdvertisedConsoleScripts()
	if len(scripts) != 2 {
		t.Fatalf("advertised console scripts = %d, want 2", len(scripts))
	}
	for _, cli := range []string{"pytest", "markdown-it"} {
		dist, script, ok := parsed.FindConsoleScript(cli)
		if !ok || dist.Name == "" || script.EntryPoint == "" {
			t.Fatalf("FindConsoleScript(%q) = (%+v, %+v, %v)", cli, dist, script, ok)
		}
	}
}

func TestNormalizeDistributionName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"PyYAML":           "pyyaml",
		"ruamel.yaml":      "ruamel-yaml",
		"markdown_it__py":  "markdown-it-py",
		"  Jinja2  ":       "jinja2",
		"":                 "",
		"   ":              "",
	}
	for in, want := range cases {
		if got := NormalizeDistributionName(in); got != want {
			t.Errorf("NormalizeDistributionName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestManifestValidationRejectsMalformedInputs(t *testing.T) {
	t.Parallel()
	if _, err := ParseImageManifest([]byte("{bad json")); err == nil {
		t.Fatal("expected error for invalid JSON")
	}

	cases := []struct {
		name   string
		mutate func(*ImageManifest)
		want   string
	}{
		{
			name:   "wrong schema version",
			mutate: func(m *ImageManifest) { m.SchemaVersion = "wrong/v0" },
			want:   "schema_version",
		},
		{
			name:   "missing image_id",
			mutate: func(m *ImageManifest) { m.ImageID = "" },
			want:   "image_id",
		},
		{
			name:   "missing built_at",
			mutate: func(m *ImageManifest) { m.BuiltAt = "" },
			want:   "built_at",
		},
		{
			name:   "missing target",
			mutate: func(m *ImageManifest) { m.Target.Triple = "" },
			want:   "target",
		},
		{
			name:   "missing interpreter",
			mutate: func(m *ImageManifest) { m.Interpreter.Version = "" },
			want:   "interpreter",
		},
		{
			name:   "missing packages_root",
			mutate: func(m *ImageManifest) { m.PackagesRoot = "" },
			want:   "packages_root",
		},
		{
			name:   "empty distributions",
			mutate: func(m *ImageManifest) { m.Distributions = nil },
			want:   "no distributions",
		},
		{
			name:   "wrong normalized name",
			mutate: func(m *ImageManifest) { m.Distributions[0].NormalizedName = "wrong" },
			want:   "normalized_name",
		},
		{
			name:   "duplicate distribution",
			mutate: func(m *ImageManifest) { m.Distributions = append(m.Distributions, m.Distributions[0]) },
			want:   "duplicate distribution",
		},
		{
			name:   "missing imports",
			mutate: func(m *ImageManifest) { m.Distributions[0].Imports = nil },
			want:   "no imports",
		},
		{
			name:   "missing dist_info_path",
			mutate: func(m *ImageManifest) { m.Distributions[0].DistInfoPath = "" },
			want:   "dist_info_path",
		},
		{
			name: "incomplete console script",
			mutate: func(m *ImageManifest) {
				m.Distributions[0].ConsoleScripts = []ConsoleScriptRecord{{Name: "broken"}}
			},
			want: "incomplete console script",
		},
		{
			name: "duplicate exposed console script",
			mutate: func(m *ImageManifest) {
				m.Distributions[0].ConsoleScripts = []ConsoleScriptRecord{{Name: "pytest", EntryPoint: "a:b", Exposed: true}}
			},
			want: "duplicate exposed console script",
		},
		{
			name:   "invalid stdlib capability",
			mutate: func(m *ImageManifest) { m.StdlibCapabilities[0].Modules = nil },
			want:   "invalid stdlib capability",
		},
		{
			name:   "duplicate stdlib capability",
			mutate: func(m *ImageManifest) { m.StdlibCapabilities = append(m.StdlibCapabilities, m.StdlibCapabilities[0]) },
			want:   "duplicate stdlib capability",
		},
		{
			name:   "invalid wasm import",
			mutate: func(m *ImageManifest) { m.WasmImports = append(m.WasmImports, WasmImportRecord{Module: "wasi"}) },
			want:   "wasm_imports",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := DefaultLaunchManifest()
			tc.mutate(&m)
			err := m.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want substring %q", err, tc.want)
			}
			if _, merr := MarshalImageManifest(m); merr == nil {
				t.Fatal("MarshalImageManifest succeeded on invalid manifest")
			}
		})
	}
}

func TestManifestLookupMisses(t *testing.T) {
	t.Parallel()
	m := DefaultLaunchManifest()
	if _, ok := m.FindDistribution(""); ok {
		t.Fatal("empty FindDistribution should miss")
	}
	if _, ok := m.FindDistribution("numpy"); ok {
		t.Fatal("numpy should miss")
	}
	if _, ok := m.FindByImport(""); ok {
		t.Fatal("empty FindByImport should miss")
	}
	if _, ok := m.FindByImport("pandas"); ok {
		t.Fatal("pandas should miss")
	}
	if _, _, ok := m.FindConsoleScript(""); ok {
		t.Fatal("empty FindConsoleScript should miss")
	}
	if _, _, ok := m.FindConsoleScript("pip"); ok {
		t.Fatal("pip should not be an exposed console script")
	}
}
