package pythoncatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultLaunchManifestRoundTripAndRoster(t *testing.T) {
	t.Parallel()
	const wantDesc = "Run helper scripts here with the standard library and fixed packages. Run project code and tests in the project environment."
	if ToolDescription != wantDesc {
		t.Fatalf("ToolDescription = %q, want %q", ToolDescription, wantDesc)
	}
	m := DefaultLaunchManifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("DefaultLaunchManifest.Validate: %v", err)
	}
	if len(m.PublicDistributions()) != LaunchPublicDistributionCount {
		t.Fatalf("public distributions = %d, want %d", len(m.PublicDistributions()), LaunchPublicDistributionCount)
	}
	if len(m.StdlibCapabilities) != 29 {
		t.Fatalf("stdlib capabilities = %d, want 29", len(m.StdlibCapabilities))
	}
	if len(m.BundledFonts) != 3 {
		t.Fatalf("bundled fonts = %d, want 3", len(m.BundledFonts))
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
		"requests":        "requests",
		"pypdf":           "pypdf",
		"json5":           "json5",
		"tzdata":          "tzdata",
		"python-dateutil": "dateutil",
		"sympy":           "sympy",
		"Pillow":          "PIL",
		"lxml":            "lxml.etree",
		"python-docx":        "docx",
		"python-pptx":        "pptx",
		"fpdf2":              "fpdf",
		"pathspec":           "pathspec",
		"networkx":           "networkx",
		"unidiff":            "unidiff",
		"croniter":           "croniter",
		"PyJWT":              "jwt",
		"python-dotenv":      "dotenv",
		"ftfy":               "ftfy",
		"sqlparse":           "sqlparse",
		"xmltodict":          "xmltodict",
		"jmespath":           "jmespath",
		"toml":               "toml",
		"tomli":              "tomli",
		"pytest-mock":        "pytest_mock",
		"freezegun":          "freezegun",
		"Faker":              "faker",
		"Markdown":           "markdown",
		"markdownify":        "markdownify",
		"html5lib":           "html5lib",
		"xlrd":               "xlrd",
		"filetype":           "filetype",
		"pytz":               "pytz",
		"icalendar":          "icalendar",
		"tabulate":           "tabulate",
		"pygal":              "pygal",
		"tqdm":               "tqdm",
		"colorama":           "colorama",
		"termcolor":          "termcolor",
		"rich":               "rich",
		"click":              "click",
		"univers":            "univers",
		"jproperties":        "jproperties",
		"license-expression": "license_expression",
		"graphql-core":       "graphql",
		"tinycss2":           "tinycss2",
		"regex":              "regex",
		"python-hcl2":        "hcl2",
		"jc":                 "jc",
		"dpkt":               "dpkt",
		"dnspython":          "dns",
		"asn1crypto":         "asn1crypto",
		"drain3":             "drain3",
		"parse":              "parse",
		"pyelftools":         "elftools",
		"numpy":              "numpy",
		"pandas":             "pandas",
	}
	if len(wantPublic) != LaunchPublicDistributionCount {
		t.Fatalf("wantPublic len = %d, want %d", len(wantPublic), LaunchPublicDistributionCount)
	}
	for _, omitted := range []string{"yamllint"} {
		if _, ok := parsed.FindDistribution(omitted); ok {
			t.Fatalf("%q must not be in the PY11 launch roster", omitted)
		}
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
		if !ok || dist.Name == "" || script != cli {
			t.Fatalf("FindConsoleScript(%q) = (%+v, %q, %v)", cli, dist, script, ok)
		}
	}
}

func TestParseRealPY02ManifestAndRenderInventory(t *testing.T) {
	t.Parallel()
	const py02JSON = `{
  "schema_version": "portable-python.image-manifest/v1",
  "image_id": "pp-cpython-3.12.0-wasi-dda9bc5b0ad0adce",
  "built_at": "2026-11-26T18:02:23Z",
  "target": "wasm32-wasi",
  "interpreter": {
    "name": "cpython",
    "version": "3.12.0",
    "wasm_path": "bin/python.wasm",
    "wasm_sha256": "5ce0cbeb843e6e5abf2d50c7158002e8333c26a40fbe27a7a52e66ee48cf64a8",
    "stdlib_path": "lib",
    "stdlib_sha256": "56519ce1508a0c3250af0bed5a7371c8ef8af996c914e9d5864b44a7a25b5c11",
    "build_inputs": {
      "release_tag": "python/3.12.0+20231211-040d5a6",
      "wasi_sdk": "20.0"
    },
    "patches": [
      {
        "target": "packages/_pytest/compat.py",
        "reason": "Treat sys.platform == 'wasi' like emscripten/win32 in get_user_id() so tmp_path fixture works without os.getuid()"
      }
    ]
  },
  "packages_root": "packages",
  "distributions": [
    {
      "name": "PyYAML",
      "normalized_name": "pyyaml",
      "version": "6.0.2",
      "public": true,
      "imports": ["yaml"],
      "backend": "pure-python",
      "features": ["safe_load", "safe_load_all", "safe_dump"],
      "console_scripts": [],
      "has_package_data": false,
      "dist_info_path": "packages/PyYAML-6.0.2.dist-info",
      "license": "MIT",
      "source_url": "https://files.pythonhosted.org/packages/source/p/pyyaml/pyyaml-6.0.2.tar.gz",
      "source_sha256": "d584d9ec91ad65861cc08d42e834324ef890a082e591037abe114850ff7bbc3e",
      "tree_sha256": "440e2861273f8b88c0fd42668bd804ed51dec19ec2e2566c339c3ddfb15b8841"
    },
    {
      "name": "pytest",
      "normalized_name": "pytest",
      "version": "8.4.1",
      "public": true,
      "imports": ["pytest", "_pytest"],
      "backend": "pure-python",
      "features": ["sys_capture", "tmp_path", "monkeypatch", "caplog"],
      "console_scripts": ["pytest"],
      "has_package_data": false,
      "dist_info_path": "packages/pytest-8.4.1.dist-info",
      "license": "MIT",
      "source_url": "https://files.pythonhosted.org/packages/pytest-8.4.1-py3-none-any.whl",
      "source_sha256": "539c70ba6fcead8e78eebbf1115e8b589e7565830d7d006a8723f19ac8a0afb7",
      "tree_sha256": "616c2f14575533e9e9598c4e43e0996ec8484b5a75ddfc71bbe9746c5698f549"
    },
    {
      "name": "pluggy",
      "normalized_name": "pluggy",
      "version": "1.6.0",
      "public": false,
      "imports": ["pluggy"],
      "backend": "pure-python",
      "features": [],
      "console_scripts": [],
      "has_package_data": false,
      "dist_info_path": "packages/pluggy-1.6.0.dist-info",
      "license": "MIT",
      "source_url": "https://files.pythonhosted.org/packages/pluggy-1.6.0-py3-none-any.whl",
      "source_sha256": "e920276dd6813095e9377c0bc5566d94c932c33b27a3e3945d8389c374dd4746",
      "tree_sha256": "3b62ad048ebeca52f2f3091b3eca3c3937368b35380567e938c0502d210f63e1"
    },
    {
      "name": "markdown-it-py",
      "normalized_name": "markdown-it-py",
      "version": "3.0.0",
      "public": true,
      "imports": ["markdown_it"],
      "backend": "pure-python",
      "features": ["commonmark"],
      "console_scripts": ["markdown-it"],
      "has_package_data": false,
      "dist_info_path": "packages/markdown_it_py-3.0.0.dist-info",
      "license": "MIT",
      "source_url": "https://files.pythonhosted.org/packages/markdown_it_py-3.0.0-py3-none-any.whl",
      "source_sha256": "aaa",
      "tree_sha256": "bbb"
    }
  ],
  "stdlib_capabilities": ["ast", "json", "pathlib", "tomllib", "zipfile"],
  "wasm_imports": [
    {
      "module": "wasi_snapshot_preview1",
      "name": "path_open",
      "kind": "func",
      "disposition": "mediated",
      "authority": "workspace_adapter"
    },
    {
      "module": "wasi_snapshot_preview1",
      "name": "sock_accept",
      "kind": "func",
      "disposition": "mediated",
      "authority": "none",
      "failure_mode": "ENOSYS/ENOTSUP (no preopened sockets)"
    }
  ]
}`
	manifestPath := filepath.Join(t.TempDir(), "image-manifest.json")
	if err := os.WriteFile(manifestPath, []byte(py02JSON), 0o600); err != nil {
		t.Fatalf("write manifest fixture: %v", err)
	}
	m, err := LoadImageManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadImageManifest: %v", err)
	}
	if m.Target != "wasm32-wasi" || m.Interpreter.Name != "cpython" || m.Interpreter.Version != "3.12.0" || m.Interpreter.StdlibPath != "lib" {
		t.Fatalf("unexpected interpreter/target: %+v / %q", m.Interpreter, m.Target)
	}
	if len(m.Interpreter.Patches) != 1 || m.Interpreter.Patches[0].Target != "packages/_pytest/compat.py" {
		t.Fatalf("unexpected patches: %+v", m.Interpreter.Patches)
	}
	if len(m.PublicDistributions()) != 3 || len(m.Distributions) != 4 {
		t.Fatalf("public=%d total=%d", len(m.PublicDistributions()), len(m.Distributions))
	}
	if len(m.WasmImports) != 2 || m.WasmImports[1].FailureMode == "" {
		t.Fatalf("unexpected wasm_imports: %+v", m.WasmImports)
	}

	// Render pip list, pip list --format=json, pip show, pip freeze, and compact help directly from PY02 manifest
	listRes, err := ExecuteReadOnlyPip(m, []string{"list"})
	if err != nil || listRes.ExitStatus != 0 || !strings.Contains(listRes.Stdout, "pluggy") || !strings.Contains(listRes.Stdout, "pytest") {
		t.Fatalf("pip list from PY02 manifest: err=%v res=%+v", err, listRes)
	}
	jsonRes, err := ExecuteReadOnlyPip(m, []string{"list", "--format=json"})
	if err != nil || jsonRes.ExitStatus != 0 || !strings.Contains(jsonRes.Stdout, `"version":"8.4.1"`) {
		t.Fatalf("pip list --format=json from PY02 manifest: err=%v res=%+v", err, jsonRes)
	}
	showRes, err := ExecuteReadOnlyPip(m, []string{"show", "pytest"})
	if err != nil || showRes.ExitStatus != 0 || !strings.Contains(showRes.Stdout, "Version: 8.4.1") || !strings.Contains(showRes.Stdout, "Location: packages") {
		t.Fatalf("pip show from PY02 manifest: err=%v res=%+v", err, showRes)
	}
	freezeRes, err := ExecuteReadOnlyPip(m, []string{"freeze"})
	if err != nil || freezeRes.ExitStatus != 0 || !strings.Contains(freezeRes.Stdout, "pytest==8.4.1\n") {
		t.Fatalf("pip freeze from PY02 manifest: err=%v res=%+v", err, freezeRes)
	}
	help := RenderPackageHelp(m)
	for _, want := range []string{
		"Portable Python 3.12.0 (cpython / wasm32-wasi)",
		"pytest 8.4.1 (import pytest, _pytest)",
		"- pytest\n",
		"- markdown-it\n",
		"ast, json, pathlib, tomllib, zipfile",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("RenderPackageHelp(PY02) missing %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "pluggy") {
		t.Errorf("RenderPackageHelp(PY02) should only list public distributions, got:\n%s", help)
	}

	// Missing file returns error
	if _, err := LoadImageManifest(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected error for missing manifest file")
	}
}

func TestNormalizeDistributionName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"PyYAML":          "pyyaml",
		"ruamel.yaml":     "ruamel-yaml",
		"markdown_it__py": "markdown-it-py",
		"  Jinja2  ":      "jinja2",
		"":                "",
		"   ":             "",
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
			mutate: func(m *ImageManifest) { m.Target = "" },
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
			name: "empty console script",
			mutate: func(m *ImageManifest) {
				m.Distributions[0].ConsoleScripts = []string{" "}
			},
			want: "empty console script",
		},
		{
			name: "duplicate console script",
			mutate: func(m *ImageManifest) {
				m.Distributions[0].ConsoleScripts = []string{"pytest"}
			},
			want: "duplicate console script",
		},
		{
			name:   "empty stdlib capability",
			mutate: func(m *ImageManifest) { m.StdlibCapabilities = append(m.StdlibCapabilities, "") },
			want:   "empty stdlib capability",
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
	if _, ok := m.FindDistribution("scipy"); ok {
		t.Fatal("scipy should miss")
	}
	if _, ok := m.FindByImport(""); ok {
		t.Fatal("empty FindByImport should miss")
	}
	if _, ok := m.FindByImport("polars"); ok {
		t.Fatal("polars should miss")
	}
	if _, _, ok := m.FindConsoleScript(""); ok {
		t.Fatal("empty FindConsoleScript should miss")
	}
	if _, _, ok := m.FindConsoleScript("pip"); ok {
		t.Fatal("pip should not be an exposed console script")
	}
}
