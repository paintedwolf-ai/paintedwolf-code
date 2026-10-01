//go:build !grammar_subset || grammar_subset_python

package grammars

import (
	"slices"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestPythonExternalScannerSpec(t *testing.T) {
	spec, ok := LookupExternalScannerSpec("python")
	if !ok {
		t.Fatal("missing python external scanner spec")
	}
	if got, want := spec.UpstreamRepo, "https://github.com/tree-sitter/tree-sitter-python"; got != want {
		t.Fatalf("python repo = %q, want %q", got, want)
	}
	if got, want := spec.Externals, []string{
		"_newline",
		"_indent",
		"_dedent",
		"string_start",
		"_string_content",
		"escape_interpolation",
		"string_end",
		"comment",
		"]",
		")",
		"}",
		"except",
	}; !slices.Equal(got, want) {
		t.Fatalf("python externals = %v, want %v", got, want)
	}
}

func TestPythonExternalScannerBindsExternalSymbolsPositionally(t *testing.T) {
	// Positional binding: external index i binds to scanner token i. The Language
	// names here are a shuffled subset; positional binding maps by position.
	lang := pythonExternalBindingTestLanguage(
		"_extension_only",
		"escape_interpolation",
		"_indent",
		"string_start",
		"_newline",
	)

	scanner, ok := PythonExternalScanner{}.ExternalScannerForLanguage(lang).(PythonExternalScanner)
	if !ok {
		t.Fatalf("PythonExternalScanner binding type = %T, want PythonExternalScanner", PythonExternalScanner{}.ExternalScannerForLanguage(lang))
	}
	if got, want := scanner.externalToToken, []int{0, 1, 2, 3, 4}; !slices.Equal(got, want) {
		t.Fatalf("python externalToToken = %v, want %v", got, want)
	}
	if got, want := scanner.externalToToken[1], pyTokIndent; got != want {
		t.Fatalf("external index 1 mapped to token %d, want %d", got, want)
	}
	if got, want := scanner.symbols[pyTokIndent], gotreesitter.Symbol(2); got != want {
		t.Fatalf("token 1 result symbol = %d, want %d", got, want)
	}

	validExternal := []bool{false, true, false, false, false}
	var semanticValid [pyTokenCount]bool
	validSemantic := scanner.remapValidSymbols(validExternal, &semanticValid)
	if !validSemantic[pyTokIndent] {
		t.Fatalf("external index 1 did not become valid semantic token 1: %v", validSemantic)
	}
}

func TestPythonExternalScannerZeroValueKeepsBuiltinSymbols(t *testing.T) {
	symbols := PythonExternalScanner{}.symbolTable()
	if got, want := symbols[pyTokNewline], pySymNewline; got != want {
		t.Fatalf("zero-value newline symbol = %d, want %d", got, want)
	}
	if got, want := symbols[pyTokStringContent], pySymStringContent; got != want {
		t.Fatalf("zero-value string-content symbol = %d, want %d", got, want)
	}
}

func pythonExternalBindingTestLanguage(names ...string) *gotreesitter.Language {
	symbolNames := make([]string, len(names)+1)
	symbols := make([]gotreesitter.Symbol, len(names))
	for i, name := range names {
		symbolNames[i+1] = name
		symbols[i] = gotreesitter.Symbol(i + 1)
	}
	return &gotreesitter.Language{
		SymbolNames:     symbolNames,
		ExternalSymbols: symbols,
	}
}
