package fileoutline_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBuildPythonOutline(t *testing.T) {
	dir := t.TempDir()
	src := strings.Join([]string{
		"import math",
		"",
		"class Vec2:",
		"    def __init__(self, x, y):",
		"        self.x = x",
		"",
		"def update(dt):",
		"    pass",
	}, "\n")
	path := filepath.Join(dir, "game.py")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "game.py")
	testutil.FailErr(t, "Build", err)
	if out.TotalLines != 8 {
		t.Fatalf("TotalLines = %d", out.TotalLines)
	}
	if len(out.Symbols) < 2 {
		t.Fatalf("symbols = %+v", out.Symbols)
	}
	if out.Diagnostics != nil {
		t.Fatalf("diagnostics = %+v want nil for tree_sitter", out.Diagnostics)
	}
}

func TestBuildCppConstantUsesDeclaredIdentifier(t *testing.T) {
	out := fileoutline.AnalyzeText(t.Context(), "cell.h", []byte("const uint32_t kNoCellIdx = 0;\n"))
	if len(out.Symbols) != 1 || out.Symbols[0].Name != "kNoCellIdx" || out.Symbols[0].Kind != "constant" {
		t.Fatalf("symbols = %+v", out.Symbols)
	}
}

func TestBuildHTMLInlineScriptOutline(t *testing.T) {
	dir := t.TempDir()
	src := strings.Join([]string{
		"<!DOCTYPE html>",
		"<html>",
		"<body>",
		"<h1>Checkers</h1>",
		"<script type=\"module\">",
		"const BOARD_SIZE = 8;",
		"function createBoard() {",
		"  return [];",
		"}",
		"function updatePieces() {",
		"  return BOARD_SIZE;",
		"}",
		"</script>",
		"</body>",
		"</html>",
	}, "\n")
	path := filepath.Join(dir, "index.html")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "index.html")
	testutil.FailErr(t, "Build", err)
	if out.Source != "tree_sitter+regex" {
		t.Fatalf("source = %q want tree_sitter+regex", out.Source)
	}
	byName := map[string]int{}
	for _, sym := range out.Symbols {
		byName[sym.Name] = sym.Line
	}
	if byName["createBoard"] != 7 {
		t.Fatalf("createBoard line = %d want 7 (symbols %+v)", byName["createBoard"], out.Symbols)
	}
	if byName["updatePieces"] != 10 {
		t.Fatalf("updatePieces line = %d want 10 (symbols %+v)", byName["updatePieces"], out.Symbols)
	}
	if byName["BOARD_SIZE"] != 6 {
		t.Fatalf("BOARD_SIZE line = %d want 6 (symbols %+v)", byName["BOARD_SIZE"], out.Symbols)
	}
}

func TestBuildCodeOutlineSkipsRegexMerge(t *testing.T) {
	dir := t.TempDir()
	src := "package main\n\nfunc main() {\n\tprintln(\"ok\")\n}\n"
	path := filepath.Join(dir, "main.go")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "main.go")
	testutil.FailErr(t, "Build", err)
	if out.Source != "tree_sitter" {
		t.Fatalf("source = %q want tree_sitter", out.Source)
	}
}

func TestBuildParseHealthClean(t *testing.T) {
	dir := t.TempDir()
	src := "package main\n\nfunc main() {\n\tprintln(\"ok\")\n}\n"
	path := filepath.Join(dir, "main.go")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "main.go")
	testutil.FailErr(t, "Build", err)
	if out.Parses == nil || !*out.Parses {
		t.Fatalf("parses = %v want true", out.Parses)
	}
	if len(out.Errors) != 0 {
		t.Fatalf("errors = %+v want none", out.Errors)
	}
	if len(out.Symbols) == 0 {
		t.Fatalf("symbols = %+v want defs unchanged", out.Symbols)
	}
}

func TestBuildParseHealthBroken(t *testing.T) {
	dir := t.TempDir()
	src := "function update() {\n  move();\n\nfunction next() {}\n"
	path := filepath.Join(dir, "app.js")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "app.js")
	testutil.FailErr(t, "Build", err)
	if out.Parses == nil || *out.Parses {
		t.Fatalf("parses = %v want false", out.Parses)
	}
	if len(out.Errors) == 0 {
		t.Fatal("errors empty for broken source")
	}
	if len(out.Symbols) == 0 {
		t.Fatalf("symbols = %+v want outline defs preserved", out.Symbols)
	}
}

func TestBuildParseHealthCleanPython(t *testing.T) {
	dir := t.TempDir()
	src := "def update():\n    pass\n"
	path := filepath.Join(dir, "game.py")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "game.py")
	testutil.FailErr(t, "Build", err)
	if out.Parses == nil || !*out.Parses {
		t.Fatalf("parses = %v want true", out.Parses)
	}
	if len(out.Errors) != 0 {
		t.Fatalf("errors = %+v want none", out.Errors)
	}
}

func TestBuildParseHealthOmittedWithoutGrammar(t *testing.T) {
	dir := t.TempDir()
	src := strings.Repeat("plain line\n", 10)
	path := filepath.Join(dir, "notes.qqq")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "notes.qqq")
	testutil.FailErr(t, "Build", err)
	if out.Parses != nil {
		t.Fatalf("parses = %v want omitted for unknown grammar", *out.Parses)
	}
	if len(out.Errors) != 0 {
		t.Fatalf("errors = %+v want none", out.Errors)
	}
}

func TestBuildPlainTextNoGrammarDiagnostics(t *testing.T) {
	dir := t.TempDir()
	src := strings.Repeat("plain line\n", 10)
	path := filepath.Join(dir, "notes.txt")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "notes.txt")
	testutil.FailErr(t, "Build", err)
	if out.Source != "regex" {
		t.Fatalf("source = %q want regex", out.Source)
	}
	if len(out.Symbols) != 0 {
		t.Fatalf("symbols = %+v", out.Symbols)
	}
	if out.Language != "" || out.Parses != nil {
		t.Fatalf("plain text claimed grammar metadata: language=%q parses=%v", out.Language, out.Parses)
	}
	if out.Diagnostics == nil || out.Diagnostics.SkipReasons.NoGrammar != 1 {
		t.Fatalf("diagnostics = %+v want no_grammar=1", out.Diagnostics)
	}
}
