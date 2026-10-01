package structrewrite

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const goSymbolFixture = `package main

func FooHandler() {
	println("foo")
}

func Close() {}

type Close struct{}
`

const tsSymbolFixture = `export function FooHandler(): void {
  console.log("foo");
}

function Close() {}
const Close = 1;
`

const pySymbolFixture = `def FooHandler():
    pass

def Close():
    pass

class Close:
    pass
`

func TestExtractSymbolSingleMatchGo(t *testing.T) {
	matches, err := ExtractSymbol(t.Context(), "go", "main.go", []byte(goSymbolFixture), SymbolRef{Name: "FooHandler"})
	if err != nil {
		t.Fatalf("ExtractSymbol: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d want 1", len(matches))
	}
	if !strings.Contains(matches[0].Text, "func FooHandler()") {
		t.Fatalf("text = %q", matches[0].Text)
	}
	if matches[0].StartRow != 2 {
		t.Fatalf("start_row = %d want 2", matches[0].StartRow)
	}
}

func TestExtractSymbolNotFoundGo(t *testing.T) {
	matches, err := ExtractSymbol(t.Context(), "go", "main.go", []byte(goSymbolFixture), SymbolRef{Name: "Missing"})
	if err != nil {
		t.Fatalf("ExtractSymbol: %v", err)
	}
	if matches != nil {
		t.Fatalf("matches = %+v want nil", matches)
	}
}

func TestExtractSymbolAmbiguousGo(t *testing.T) {
	matches, err := ExtractSymbol(t.Context(), "go", "main.go", []byte(goSymbolFixture), SymbolRef{Name: "Close"})
	if err != nil {
		t.Fatalf("ExtractSymbol: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %d want 2", len(matches))
	}
}

func TestExtractSymbolKindDisambiguatesGo(t *testing.T) {
	matches, err := ExtractSymbol(t.Context(), "go", "main.go", []byte(goSymbolFixture), SymbolRef{Name: "Close", Kind: "type"})
	if err != nil {
		t.Fatalf("ExtractSymbol: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d want 1", len(matches))
	}
	if !strings.HasPrefix(strings.TrimSpace(matches[0].Text), "type Close") {
		t.Fatalf("text = %q", matches[0].Text)
	}
}

func TestExtractSymbolTypeScript(t *testing.T) {
	matches, err := ExtractSymbol(t.Context(), "typescript", "handler.ts", []byte(tsSymbolFixture), SymbolRef{Name: "FooHandler"})
	if err != nil {
		t.Fatalf("ExtractSymbol: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d want 1", len(matches))
	}
	if !strings.HasPrefix(strings.TrimSpace(matches[0].Text), "export function FooHandler") {
		t.Fatalf("exported function span must include export, text = %q", matches[0].Text)
	}
}

func TestExtractSymbolTypeScriptMethodStaysInsideClass(t *testing.T) {
	src := "export class Engine {\n  do(a: number) { return a; }\n}\n"
	matches, err := ExtractSymbol(t.Context(), "typescript", "engine.ts", []byte(src), SymbolRef{Name: "do"})
	if err != nil {
		t.Fatalf("ExtractSymbol: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d want 1", len(matches))
	}
	if strings.Contains(matches[0].Text, "class Engine") {
		t.Fatalf("method span must not expand to the class: %q", matches[0].Text)
	}
	if !strings.Contains(matches[0].Text, "do(a: number)") {
		t.Fatalf("text = %q", matches[0].Text)
	}
}

func TestExtractSymbolPythonDecorator(t *testing.T) {
	src := "@app.get('/')\ndef FooHandler():\n    pass\n"
	matches, err := ExtractSymbol(t.Context(), "python", "handler.py", []byte(src), SymbolRef{Name: "FooHandler"})
	if err != nil {
		t.Fatalf("ExtractSymbol: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d want 1", len(matches))
	}
	if !strings.Contains(matches[0].Text, "@app.get") {
		t.Fatalf("decorated span must include the decorator: %q", matches[0].Text)
	}
}

func TestExtractSymbolGoConstBlockKeepsSiblings(t *testing.T) {
	src := "package p\n\nconst (\n\tA = 1\n\tB = 2\n)\n"
	matches, err := ExtractSymbol(t.Context(), "go", "c.go", []byte(src), SymbolRef{Name: "A"})
	if err != nil {
		t.Fatalf("ExtractSymbol: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d want 1", len(matches))
	}
	if strings.Contains(matches[0].Text, "B = 2") {
		t.Fatalf("const sibling must stay outside the span: %q", matches[0].Text)
	}
}

func TestExtractSymbolPython(t *testing.T) {
	matches, err := ExtractSymbol(t.Context(), "python", "handler.py", []byte(pySymbolFixture), SymbolRef{Name: "FooHandler"})
	if err != nil {
		t.Fatalf("ExtractSymbol: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d want 1", len(matches))
	}
	if !strings.Contains(matches[0].Text, "def FooHandler") {
		t.Fatalf("text = %q", matches[0].Text)
	}
}

func TestExtractSymbolUnsupportedLanguage(t *testing.T) {
	matches, err := ExtractSymbol(t.Context(), "", "plain.zzzzz", []byte("hello"), SymbolRef{Name: "hello"})
	if err != nil {
		t.Fatalf("ExtractSymbol: %v", err)
	}
	if matches != nil {
		t.Fatalf("matches = %+v want nil", matches)
	}
}

func TestExtractSymbolQualifiedFallsBackToLastSegment(t *testing.T) {
	matches, err := ExtractSymbol(t.Context(), "go", "main.go", []byte(goSymbolFixture), SymbolRef{Name: "main.FooHandler"})
	if err != nil {
		t.Fatalf("ExtractSymbol: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d want 1", len(matches))
	}
	if !strings.Contains(matches[0].Text, "func FooHandler()") {
		t.Fatalf("text = %q", matches[0].Text)
	}
}

func TestListDefinitionNamesGo(t *testing.T) {
	names, err := ListDefinitionNames(t.Context(), "go", "main.go", []byte(goSymbolFixture))
	testutil.FailErr(t, "list definitions", err)
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "FooHandler") || !strings.Contains(joined, "Close") {
		t.Fatalf("names = %v", names)
	}
}

func TestExtractSymbolEmptyName(t *testing.T) {
	_, err := ExtractSymbol(t.Context(), "go", "main.go", []byte(goSymbolFixture), SymbolRef{})
	if err == nil {
		t.Fatal("expected error for empty symbol name")
	}
}
