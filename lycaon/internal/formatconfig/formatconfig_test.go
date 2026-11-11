package formatconfig

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/editorconfig"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write "+rel, os.WriteFile(abs, []byte(body), 0o600))
	}
	return root
}

func resolveTool(t *testing.T, files map[string]string, rel string, id api.FormatTool) (Source, bool) {
	t.Helper()
	sources, err := Resolve(writeFiles(t, files), rel)
	testutil.FailErr(t, "resolve", err)
	for _, s := range sources {
		if s.Tool == id {
			return s, true
		}
	}
	return Source{}, false
}

func TestCatalogNamesEveryWireToolOnce(t *testing.T) {
	seen := map[api.FormatTool]bool{api.FormatToolEditorConfig: true}
	for _, tool := range catalog {
		if seen[tool.id] {
			t.Fatalf("%s is listed twice", tool.id)
		}
		seen[tool.id] = true
		if len(tool.files) == 0 || tool.settings == nil {
			t.Fatalf("%s has no files or settings reader", tool.id)
		}
	}
	for _, id := range api.AllFormatToolValues() {
		if !seen[id] {
			t.Fatalf("wire tool %s has no catalog entry", id)
		}
	}
	if len(seen) != len(api.AllFormatToolValues()) {
		t.Fatalf("catalog has %d tools, wire has %d", len(seen), len(api.AllFormatToolValues()))
	}
	names := ConfigNames()
	for _, tool := range catalog {
		for _, f := range tool.files {
			if !slices.Contains(names, f.name) {
				t.Fatalf("config names miss %s", f.name)
			}
		}
	}
	if len(names) != len(slices.Compact(slices.Sorted(slices.Values(names)))) {
		t.Fatalf("config names repeat: %v", names)
	}
}

func TestNearestConfigurationWins(t *testing.T) {
	got, ok := resolveTool(t, map[string]string{
		".prettierrc":         "printWidth: 120\n",
		"web/.prettierrc.json": `{"printWidth": 100, "useTabs": true}`,
	}, "web/src/app.ts", api.FormatToolPrettier)
	if !ok || got.Configs[0] != "web/.prettierrc.json" || got.Settings.LineWidth != 100 ||
		got.Settings.IndentStyle != editorconfig.IndentTab || got.Settings.IndentSize != 2 {
		t.Fatalf("prettier = %+v", got)
	}
}

func TestDocumentedDefaultsFillWhatTheConfigurationLeavesOut(t *testing.T) {
	got, ok := resolveTool(t, map[string]string{".prettierrc": "semi: false\n"}, "a.ts", api.FormatToolPrettier)
	if !ok || got.Settings.LineWidth != 80 || got.Settings.EndOfLine != "lf" {
		t.Fatalf("prettier = %+v", got)
	}
	got, ok = resolveTool(t, map[string]string{"rustfmt.toml": "edition = \"2021\"\n"}, "src/lib.rs", api.FormatToolRustfmt)
	if !ok || got.Settings.LineWidth != 100 || got.Settings.IndentSize != 4 {
		t.Fatalf("rustfmt = %+v", got)
	}
}

func TestAFileWithoutTheToolsSectionDoesNotStopTheSearch(t *testing.T) {
	got, ok := resolveTool(t, map[string]string{
		"package.json":     `{"prettier": {"printWidth": 90}}`,
		"pkg/package.json": `{"name": "pkg"}`,
	}, "pkg/index.js", api.FormatToolPrettier)
	if !ok || got.Configs[0] != "package.json" || got.Settings.LineWidth != 90 {
		t.Fatalf("prettier = %+v", got)
	}
	if _, ok := resolveTool(t, map[string]string{"pyproject.toml": "[tool.ruff]\nline-length = 100\n"}, "a.py", api.FormatToolBlack); ok {
		t.Fatal("a pyproject without [tool.black] configured Black")
	}
}

func TestANearerConfigurationTheHostCannotReadDeclaresNothing(t *testing.T) {
	if got, ok := resolveTool(t, map[string]string{
		".prettierrc":           "printWidth: 120\n",
		"web/prettier.config.js": "export default { printWidth: 100 }\n",
	}, "web/app.ts", api.FormatToolPrettier); ok {
		t.Fatalf("prettier = %+v, want nothing in effect", got)
	}
	if got, ok := resolveTool(t, map[string]string{"biome.json": "{ not json"}, "a.ts", api.FormatToolBiome); ok {
		t.Fatalf("malformed biome.json gave %+v", got)
	}
}

func TestEachFormatIsRead(t *testing.T) {
	cases := []struct {
		files map[string]string
		path  string
		tool  api.FormatTool
		want  Settings
	}{
		{map[string]string{"biome.jsonc": "{\n  // width\n  \"formatter\": {\"lineWidth\": 110, \"indentStyle\": \"space\",},\n}\n"},
			"a.ts", api.FormatToolBiome, Settings{LineWidth: 110, IndentStyle: editorconfig.IndentSpace, IndentSize: 2, EndOfLine: "lf"}},
		{map[string]string{".clang-format": "---\nLanguage: Proto\nColumnLimit: 60\n---\nBasedOnStyle: Microsoft\nUseTab: Never\nLineEnding: CRLF\n"},
			"a.cc", api.FormatToolClangFormat, Settings{LineWidth: 120, IndentStyle: editorconfig.IndentSpace, EndOfLine: "crlf"}},
		{map[string]string{".clang-format": "ColumnLimit: 0\n"},
			"a.cc", api.FormatToolClangFormat, Settings{}},
		{map[string]string{".sqlfluff": "[sqlfluff]\nmax_line_length = 120\n[sqlfluff:indentation]\nindent_unit = tab\n"},
			"q.sql", api.FormatToolSqlfluff, Settings{LineWidth: 120, IndentStyle: editorconfig.IndentTab, IndentSize: 4}},
		{map[string]string{".scalafmt.conf": "version = \"3.8.0\"\nmaxColumn = 100\n"},
			"A.scala", api.FormatToolScalafmt, Settings{LineWidth: 100}},
		{map[string]string{".rubocop.yml": "Layout/LineLength:\n  Max: 100\n"},
			"a.rb", api.FormatToolRubocop, Settings{LineWidth: 100, IndentStyle: editorconfig.IndentSpace, IndentSize: 2}},
		{map[string]string{".swift-format": `{"lineLength": 120, "indentation": {"tabs": 1}}`},
			"a.swift", api.FormatToolSwiftFormat, Settings{LineWidth: 120, IndentStyle: editorconfig.IndentTab, IndentSizeTab: true}},
		{map[string]string{"taplo.toml": "[formatting]\ncolumn_width = 100\nindent_string = \"    \"\ncrlf = true\n"},
			"Cargo.toml", api.FormatToolTaplo, Settings{LineWidth: 100, IndentStyle: editorconfig.IndentSpace, IndentSize: 4, EndOfLine: "crlf"}},
	}
	for _, c := range cases {
		got, ok := resolveTool(t, c.files, c.path, c.tool)
		if !ok || got.Settings != c.want {
			t.Errorf("%s: got %+v (found %v), want %+v", c.tool, got.Settings, ok, c.want)
		}
	}
}

func TestEditorConfigReportsLineWidthAndContributingFiles(t *testing.T) {
	got, ok := resolveTool(t, map[string]string{
		".editorconfig":     "root = true\n[*]\nmax_line_length = 120\n[*.md]\nmax_line_length = off\n",
		"pkg/.editorconfig": "[*.go]\nindent_style = tab\n",
	}, "pkg/main.go", api.FormatToolEditorConfig)
	if !ok || got.Settings.LineWidth != 120 || !slices.Equal(got.Configs, []string{"pkg/.editorconfig", ".editorconfig"}) {
		t.Fatalf("editorconfig = %+v", got)
	}
	got, _ = resolveTool(t, map[string]string{".editorconfig": "[*]\nmax_line_length = 120\n[*.md]\nmax_line_length = off\n"},
		"README.md", api.FormatToolEditorConfig)
	if got.Settings.LineWidth != 0 {
		t.Fatalf("off kept width %d", got.Settings.LineWidth)
	}
}
