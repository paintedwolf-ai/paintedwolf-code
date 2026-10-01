package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const fakeGeneratedGrammarJSON = `{
  "name": "cli_test",
  "rules": {
    "source_file": {"type": "STRING", "value": "ok"}
  },
  "extras": [],
  "conflicts": [],
  "externals": [
    {"type": "SYMBOL", "name": "indent"}
  ],
  "inline": [],
  "supertypes": []
}`

func TestParseTreeSitterCLIVersion(t *testing.T) {
	tests := []struct {
		output string
		major  int
		minor  int
	}{
		{output: "tree-sitter 0.26.6\n", major: 0, minor: 26},
		{output: "tree-sitter v1.2.0", major: 1, minor: 2},
	}
	for _, test := range tests {
		major, minor, err := parseTreeSitterCLIVersion(test.output)
		if err != nil {
			t.Fatalf("parseTreeSitterCLIVersion(%q): %v", test.output, err)
		}
		if major != test.major || minor != test.minor {
			t.Fatalf(
				"parseTreeSitterCLIVersion(%q) = %d.%d, want %d.%d",
				test.output,
				major,
				minor,
				test.major,
				test.minor,
			)
		}
	}
}

func TestImportGrammarJSWithCLIUsesCanonicalJSON(t *testing.T) {
	tmp := t.TempDir()
	grammarPath := filepath.Join(tmp, "grammar.js")
	if err := os.WriteFile(grammarPath, []byte("module.exports = grammar({});\n"), 0o644); err != nil {
		t.Fatalf("write grammar.js: %v", err)
	}
	argsPath := filepath.Join(tmp, "args")
	dirPath := filepath.Join(tmp, "dir")
	outputPath := filepath.Join(tmp, "output")
	t.Setenv("FAKE_ARGS_PATH", argsPath)
	t.Setenv("FAKE_DIR_PATH", dirPath)
	t.Setenv("FAKE_OUTPUT_PATH", outputPath)

	cli := writeFakeTreeSitterCLI(t, tmp, "0.26.6", fakeGeneratedGrammarJSON, 0)
	g, err := importGrammarJSWithCLI(grammarPath, cli)
	if err != nil {
		t.Fatalf("importGrammarJSWithCLI: %v", err)
	}
	if g.Name != "cli_test" {
		t.Fatalf("grammar name = %q, want cli_test", g.Name)
	}
	if len(g.Externals) != 1 || g.Externals[0].Value != "indent" {
		t.Fatalf("external tokens = %#v, want indent", g.Externals)
	}

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read fake arguments: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	wantPrefix := []string{"generate", "--no-parser", "--output"}
	if len(lines) != 5 || !reflect.DeepEqual(lines[:3], wantPrefix) || lines[4] != "grammar.js" {
		t.Fatalf("tree-sitter arguments = %#v", lines)
	}
	if dir, err := os.ReadFile(dirPath); err != nil {
		t.Fatalf("read fake working directory: %v", err)
	} else if strings.TrimSpace(string(dir)) != tmp {
		t.Fatalf("tree-sitter directory = %q, want %q", strings.TrimSpace(string(dir)), tmp)
	}
	if output, err := os.ReadFile(outputPath); err != nil {
		t.Fatalf("read fake output directory: %v", err)
	} else {
		outputDir := strings.TrimSpace(string(output))
		if lines[3] != outputDir {
			t.Fatalf("argument output = %q, recorded output = %q", lines[3], outputDir)
		}
		if _, err := os.Stat(outputDir); !os.IsNotExist(err) {
			t.Fatalf("temporary output still exists: %v", err)
		}
	}
}

func TestImportGrammarJSWithCLIRejectsOldVersion(t *testing.T) {
	tmp := t.TempDir()
	grammarPath := filepath.Join(tmp, "grammar.js")
	if err := os.WriteFile(grammarPath, []byte("module.exports = grammar({});\n"), 0o644); err != nil {
		t.Fatalf("write grammar.js: %v", err)
	}
	cli := writeFakeTreeSitterCLI(t, tmp, "0.25.10", fakeGeneratedGrammarJSON, 0)
	_, err := importGrammarJSWithCLI(grammarPath, cli)
	if err == nil || !strings.Contains(err.Error(), "0.26 or newer") {
		t.Fatalf("error = %v, want minimum version error", err)
	}
}

func TestImportGrammarJSWithCLIReportsGenerateFailure(t *testing.T) {
	tmp := t.TempDir()
	grammarPath := filepath.Join(tmp, "grammar.js")
	if err := os.WriteFile(grammarPath, []byte("module.exports = grammar({});\n"), 0o644); err != nil {
		t.Fatalf("write grammar.js: %v", err)
	}
	cli := writeFakeTreeSitterCLI(t, tmp, "0.26.6", "", 7)
	_, err := importGrammarJSWithCLI(grammarPath, cli)
	if err == nil || !strings.Contains(err.Error(), "tree-sitter generate") ||
		!strings.Contains(err.Error(), "fake generation failed") {
		t.Fatalf("error = %v, want generation output", err)
	}
}

func TestImportGrammarJSWithCLIReportsMissingJavaScriptRuntime(t *testing.T) {
	tmp := t.TempDir()
	grammarPath := filepath.Join(tmp, "grammar.js")
	if err := os.WriteFile(grammarPath, []byte("module.exports = grammar({});\n"), 0o644); err != nil {
		t.Fatalf("write grammar.js: %v", err)
	}
	t.Setenv("FAKE_GENERATE_ERROR", "Failed to load grammar.js -- Failed to run `node` -- No such file or directory")
	cli := writeFakeTreeSitterCLI(t, tmp, "0.26.6", "", 7)
	_, err := importGrammarJSWithCLI(grammarPath, cli)
	if err == nil ||
		!strings.Contains(err.Error(), "JavaScript runtime such as Node") ||
		!strings.Contains(err.Error(), "tree-sitter generate") {
		t.Fatalf("error = %v, want JavaScript runtime requirement and generation context", err)
	}
}

func TestImportGrammarJSWithCLIRejectsMissingCapability(t *testing.T) {
	tmp := t.TempDir()
	grammarPath := filepath.Join(tmp, "grammar.js")
	if err := os.WriteFile(grammarPath, []byte("module.exports = grammar({});\n"), 0o644); err != nil {
		t.Fatalf("write grammar.js: %v", err)
	}
	cli := writeFakeTreeSitterCLI(t, tmp, "0.26.6", fakeGeneratedGrammarJSON, 0)
	t.Setenv("FAKE_GENERATE_HELP", "--no-parser")
	_, err := importGrammarJSWithCLI(grammarPath, cli)
	if err == nil || !strings.Contains(err.Error(), "lacks required generate flags") ||
		!strings.Contains(err.Error(), "--output") {
		t.Fatalf("error = %v, want missing --output error", err)
	}
}

func writeFakeTreeSitterCLI(t *testing.T, dir, version, grammarJSON string, generateStatus int) string {
	t.Helper()
	path := filepath.Join(dir, "tree-sitter")
	script := `#!/bin/sh
set -eu
if test "$1" = "--version"; then
  printf 'tree-sitter %s\n' "$FAKE_VERSION"
  exit 0
fi
if test "$1" = "generate" && test "$2" = "--help"; then
  printf '%s\n' "$FAKE_GENERATE_HELP"
  exit 0
fi
printf '%s\n' "$@" > "${FAKE_ARGS_PATH:-/dev/null}"
printf '%s\n' "$PWD" > "${FAKE_DIR_PATH:-/dev/null}"
if test "$FAKE_GENERATE_STATUS" != "0"; then
  printf '%s\n' "${FAKE_GENERATE_ERROR:-fake generation failed}" >&2
  exit "$FAKE_GENERATE_STATUS"
fi
output=
while test "$#" -gt 0; do
  if test "$1" = "--output"; then
    output=$2
    break
  fi
  shift
done
test -n "$output"
printf '%s\n' "$output" > "${FAKE_OUTPUT_PATH:-/dev/null}"
mkdir -p "$output"
printf '%s' "$FAKE_GRAMMAR_JSON" > "$output/grammar.json"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tree-sitter CLI: %v", err)
	}
	t.Setenv("FAKE_VERSION", version)
	t.Setenv("FAKE_GENERATE_HELP", "--no-parser\n--output")
	t.Setenv("FAKE_GRAMMAR_JSON", grammarJSON)
	t.Setenv("FAKE_GENERATE_STATUS", strconv.Itoa(generateStatus))
	return path
}
