//go:build !grammar_subset

package grammars

import (
	"slices"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// parseHasError parses src with lang and reports whether the resulting tree has
// any error, returning the s-expression for diagnostics.
func parseHasError(t *testing.T, lang *gotreesitter.Language, src string) (bool, string) {
	t.Helper()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse returned nil root")
	}
	return root.HasError(), root.SExpr(lang)
}

// TestKotlinSafeNavParsesClean is the audit's regression witness. Under the old
// by-name binder the kotlin safe-nav external (spec rule "safe_nav") never bound
// because the Language display name is "\?.", so `?.` mislexed and every use
// produced a spurious ERROR. Positional binding lands safe_nav on scanner token 2.
//
// This test FAILS on main (err on the safe-nav cases) and PASSES with the fix.
func TestKotlinSafeNavParsesClean(t *testing.T) {
	lang := KotlinLanguage()
	cases := []struct {
		name string
		src  string
	}{
		{"safe_nav", "fun f() { val x = a?.b }"},
		{"safe_nav_chain", "fun f() { val x = a?.b?.c }"},
		{"plain_dot_control", "fun f() { val x = a.b }"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hasErr, sexpr := parseHasError(t, lang, tc.src)
			if hasErr {
				t.Fatalf("kotlin %q parsed with errors:\n%s", tc.src, sexpr)
			}
		})
	}
}

// TestSwiftDisplayMismatchedExternalsParseClean guards the 18 swift externals
// whose Language display name differs from their spec rule name (->, ??, as?,
// as!, ...). They were previously rescued only by the GeneratedByGrammargen
// provenance flag; positional binding makes the rescue provenance-independent.
func TestSwiftDisplayMismatchedExternalsParseClean(t *testing.T) {
	lang := SwiftLanguage()
	cases := []struct {
		name string
		src  string
	}{
		{"nil_coalescing", "let x = a ?? b"},
		{"as_quest", "let y = a as? B"},
		{"as_bang", "let z = a as! B"},
		{"arrow", "func f() -> Int { return 0 }"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hasErr, sexpr := parseHasError(t, lang, tc.src)
			if hasErr {
				t.Fatalf("swift %q parsed with errors:\n%s", tc.src, sexpr)
			}
		})
	}
}

// TestDartAndRustScannerWitnessesParseClean closes the audit's lower-confidence
// edges: dart nestable/doc block comments and rust raw strings + lifetimes all
// flow through externals whose binding must remain intact under positional.
func TestDartAndRustScannerWitnessesParseClean(t *testing.T) {
	dart := DartLanguage()
	for _, tc := range []struct{ name, src string }{
		{"doc_block_comment", "/** x */\nvoid main() {}"},
		{"nested_block_comment", "void main() { /* a /* b */ c */ }"},
	} {
		t.Run("dart_"+tc.name, func(t *testing.T) {
			hasErr, sexpr := parseHasError(t, dart, tc.src)
			if hasErr {
				t.Fatalf("dart %q parsed with errors:\n%s", tc.src, sexpr)
			}
		})
	}

	rust := RustLanguage()
	for _, tc := range []struct{ name, src string }{
		{"raw_string", `fn main() { let s = r"x"; }`},
		{"raw_string_hash", `fn main() { let s = r#"x"#; }`},
		{"lifetimes", "fn f<'a>(x: &'a str) -> &'a str { x }"},
	} {
		t.Run("rust_"+tc.name, func(t *testing.T) {
			hasErr, sexpr := parseHasError(t, rust, tc.src)
			if hasErr {
				t.Fatalf("rust %q parsed with errors:\n%s", tc.src, sexpr)
			}
		})
	}
}

// TestRealLanguageExternalBindingTablesArePositional pins the exact externalToToken
// table for every name-binding language. python/swift/dart/rust are unchanged from
// the by-name binder; kotlin gains its previously-dropped safe_nav slot (index 2).
//
// Each language block also asserts the scanner's post-bind symbols table equals
// that scanner's package-level Default*SymTable. For every canonical (checked-in)
// grammar blob, Language.ExternalSymbols is in the same order as the hand-written
// scanner's Default*SymTable, so this equality is non-tautological: positional
// binding sets symbols[i] = lang.ExternalSymbols[i], while Default*SymTable[i] is
// the scanner author's independently recorded expectation for that same slot. A
// future external REORDER (two upstream externals swapping positions in a grammar
// regeneration) would still produce the trivial identity externalToToken
// ([0,1,2,...]) checked above -- that check never looks at symbol values, so it
// cannot see a reorder -- but the bound symbols would then disagree with
// Default*SymTable and this assertion would fail. All five scanners expose their
// default table as a package-level var (kotlinDefaultSymTable, pyDefaultSymTable,
// swtDefaultSymTable, dartDefaultSymTable, rustDefaultSymTable), so every language
// below compares its post-bind symbols directly against that var; none needed the
// lang.ExternalSymbols[i]-by-index fallback.
//
// KNOWN FAILING for python and swift as of this writing, and NOT a positional-
// binding bug: both python_scanner.go's pyDefaultSymTable and swift_scanner.go's
// swtDefaultSymTable are stale relative to the currently checked-in python.bin /
// swift.bin (confirmed pre-existing: both scanner files are untouched in this
// tree's uncommitted diff, and the drift reproduces in isolation with no other
// test bodies executed, ruling out cross-test mutation of the cached Language
// singleton). python: all 12 slots are shifted (e.g. index 0 "_newline" is live
// symbol 101 vs. pyDefaultSymTable's 102). swift: 32 of 33 slots are shifted by a
// uniform +10, and index 30 ("_directive_else") carries an unrelated, independent
// bug: swtSymDirectiveElse = 530, wildly outside the ~180-222 range of every
// neighboring swift external symbol. kotlin, dart, and rust match their default
// tables exactly. This assertion is deliberately left strict rather than softened
// for python/swift: weakening it would defeat its purpose for precisely the two
// languages where it has already caught real drift. Fixing pySym*/swtSym* lives in
// python_scanner.go/swift_scanner.go, outside this test file's scope.
func TestRealLanguageExternalBindingTablesArePositional(t *testing.T) {
	kotlin := KotlinExternalScanner{}.ExternalScannerForLanguage(KotlinLanguage()).(KotlinExternalScanner)
	if got, want := kotlin.externalToToken, []int{0, 1, 2, 3, 4, 5, 6, 7, 8}; !slices.Equal(got, want) {
		t.Fatalf("kotlin externalToToken = %v, want %v (safe_nav at index 2 must bind)", got, want)
	}
	if got, want := kotlin.symbols, kotlinDefaultSymTable; got != want {
		t.Fatalf("kotlin post-bind symbols = %v, want default table %v (an ExternalSymbols reorder would still pass the externalToToken check above)", got, want)
	}

	python := PythonExternalScanner{}.ExternalScannerForLanguage(PythonLanguage()).(PythonExternalScanner)
	if got, want := python.externalToToken, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}; !slices.Equal(got, want) {
		t.Fatalf("python externalToToken = %v, want %v", got, want)
	}
	if got, want := python.symbols, pyDefaultSymTable; got != want {
		t.Fatalf("python post-bind symbols = %v, want default table %v", got, want)
	}

	swift := SwiftExternalScanner{}.ExternalScannerForLanguage(SwiftLanguage()).(SwiftExternalScanner)
	wantSwift := make([]int, swtTokenCount)
	for i := range wantSwift {
		wantSwift[i] = i
	}
	if got := swift.externalToToken; !slices.Equal(got, wantSwift) {
		t.Fatalf("swift externalToToken = %v, want %v", got, wantSwift)
	}
	if got, want := swift.symbols, swtDefaultSymTable; got != want {
		t.Fatalf("swift post-bind symbols = %v, want default table %v", got, want)
	}

	dart := DartExternalScanner{}.ExternalScannerForLanguage(DartLanguage()).(DartExternalScanner)
	if got, want := dart.externalToToken, []int{0, 1, 2, 3, 4, 5, 6}; !slices.Equal(got, want) {
		t.Fatalf("dart externalToToken = %v, want %v", got, want)
	}
	if got, want := dart.symbols, dartDefaultSymTable; got != want {
		t.Fatalf("dart post-bind symbols = %v, want default table %v", got, want)
	}

	rust := RustExternalScanner{}.ExternalScannerForLanguage(RustLanguage()).(RustExternalScanner)
	if got, want := rust.externalToToken, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}; !slices.Equal(got, want) {
		t.Fatalf("rust externalToToken = %v, want %v", got, want)
	}
	if got, want := rust.symbols, rustDefaultSymTable; got != want {
		t.Fatalf("rust post-bind symbols = %v, want default table %v", got, want)
	}

	// javascript: pins the currently-shipped javascript.bin's binding as a
	// permanent regression guard. This equality is non-tautological the same
	// way as the other five languages: positional binding sets symbols[i] =
	// lang.ExternalSymbols[i], while jsDefaultSymTable[i] is jsSym*'s
	// independently recorded expectation for that same slot. If a future
	// javascript.bin regen shifts the automaton's absolute symbol numbering
	// (as it does today at grammargen HEAD -- see
	// TestJavaScriptExternalScannerBindsPositionally for the synthetic-Language
	// reproduction of that shift), this externalToToken check still passes
	// (positional binding is index-based, not value-based) but the CI blob-swap
	// proof in the scanner-adapt-corruption spore is what actually matters for
	// end-to-end correctness -- this test only guards the shipped blob's
	// binding, not a hypothetical regenerated one.
	javascript := JavaScriptExternalScanner{}.ExternalScannerForLanguage(JavascriptLanguage()).(JavaScriptExternalScanner)
	if got, want := javascript.externalToToken, []int{0, 1, 2, 3, 4, 5, 6, 7}; !slices.Equal(got, want) {
		t.Fatalf("javascript externalToToken = %v, want %v", got, want)
	}
	if got, want := javascript.symbols, jsDefaultSymTable; got != want {
		t.Fatalf("javascript post-bind symbols = %v, want default table %v", got, want)
	}

	// typescript and tsx: pin the currently-shipped typescript.bin/tsx.bin
	// bindings as permanent regression guards, the same way as javascript
	// above. Both carry the same 10 externals (javascript's 8 plus
	// _function_signature_automatic_semicolon and __error_recovery), so a
	// future blob regen that shifts the automaton's absolute symbol
	// numbering still passes this externalToToken check (index-based, not
	// value-based) but would only pass the symbols equality below if the
	// external declaration order is unchanged.
	typescript := TypeScriptExternalScanner{}.ExternalScannerForLanguage(TypescriptLanguage()).(TypeScriptExternalScanner)
	if got, want := typescript.externalToToken, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}; !slices.Equal(got, want) {
		t.Fatalf("typescript externalToToken = %v, want %v", got, want)
	}
	if got, want := typescript.symbols, tsDefaultSymTable; got != want {
		t.Fatalf("typescript post-bind symbols = %v, want default table %v", got, want)
	}

	tsx := TsxExternalScanner{}.ExternalScannerForLanguage(TsxLanguage()).(TsxExternalScanner)
	if got, want := tsx.externalToToken, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}; !slices.Equal(got, want) {
		t.Fatalf("tsx externalToToken = %v, want %v", got, want)
	}
	if got, want := tsx.symbols, tsxDefaultSymTable; got != want {
		t.Fatalf("tsx post-bind symbols = %v, want default table %v", got, want)
	}
}

// Note: the pure-unit drift and length-mismatch tests for
// bindExternalScannerSymbolNames (no real-language dependency) live in
// external_scanner_binding_test.go, not here. This file is gated !grammar_subset
// only, so a test with zero real-language dependency has no reason to be invisible
// to every grammar_subset build; the sibling file's broader tag keeps it visible to
// grammar_subset builds that select kotlin+swift.
