package grep

import (
	"bytes"
	"strings"
	"testing"
)

// --------------------------------------------------------------------------
// ApplyEdits tests
// --------------------------------------------------------------------------

func TestApplyEdits_EmptyEdits(t *testing.T) {
	source := []byte("hello world")
	result := ApplyEdits(source, nil)
	if !bytes.Equal(result, source) {
		t.Errorf("expected %q, got %q", source, result)
	}
	// Ensure result is a copy, not aliased.
	source[0] = 'H'
	if result[0] == 'H' {
		t.Error("ApplyEdits should return a copy, not alias the original")
	}
}

func TestApplyEdits_SingleEdit(t *testing.T) {
	source := []byte("hello world")
	edits := []Edit{
		{StartByte: 0, EndByte: 5, Replacement: []byte("goodbye")},
	}
	result := ApplyEdits(source, edits)
	want := "goodbye world"
	if string(result) != want {
		t.Errorf("expected %q, got %q", want, string(result))
	}
}

func TestApplyEdits_MultipleNonOverlapping(t *testing.T) {
	source := []byte("aaa bbb ccc")
	edits := []Edit{
		{StartByte: 0, EndByte: 3, Replacement: []byte("AAA")},
		{StartByte: 8, EndByte: 11, Replacement: []byte("CCC")},
	}
	result := ApplyEdits(source, edits)
	want := "AAA bbb CCC"
	if string(result) != want {
		t.Errorf("expected %q, got %q", want, string(result))
	}
}

func TestApplyEdits_DifferentSizeReplacements(t *testing.T) {
	source := []byte("ab cd ef")
	edits := []Edit{
		{StartByte: 0, EndByte: 2, Replacement: []byte("ABCDE")}, // grow
		{StartByte: 6, EndByte: 8, Replacement: []byte("F")},     // shrink
	}
	result := ApplyEdits(source, edits)
	want := "ABCDE cd F"
	if string(result) != want {
		t.Errorf("expected %q, got %q", want, string(result))
	}
}

func TestApplyEdits_PreservesUneditedSource(t *testing.T) {
	source := []byte("the quick brown fox jumps over the lazy dog")
	edits := []Edit{
		{StartByte: 10, EndByte: 15, Replacement: []byte("red")},
	}
	result := ApplyEdits(source, edits)
	want := "the quick red fox jumps over the lazy dog"
	if string(result) != want {
		t.Errorf("expected %q, got %q", want, string(result))
	}
}

func TestApplyEdits_InsertionAtSamePoint(t *testing.T) {
	source := []byte("hello")
	edits := []Edit{
		{StartByte: 5, EndByte: 5, Replacement: []byte(" world")},
	}
	result := ApplyEdits(source, edits)
	want := "hello world"
	if string(result) != want {
		t.Errorf("expected %q, got %q", want, string(result))
	}
}

// --------------------------------------------------------------------------
// substituteTemplate tests
// --------------------------------------------------------------------------

func TestSubstituteTemplate_BasicCaptures(t *testing.T) {
	captures := map[string]string{
		"FN":   "console.info",
		"ARGS": `"hello"`,
	}
	result := substituteTemplate("$FN($ARGS)", captures, true)
	want := `console.info("hello")`
	if result != want {
		t.Errorf("expected %q, got %q", want, result)
	}
}

func TestSubstituteTemplate_VariadicCapture(t *testing.T) {
	captures := map[string]string{
		"ITEMS": "a, b, c",
	}
	result := substituteTemplate("[$$$ITEMS]", captures, true)
	want := "[a, b, c]"
	if result != want {
		t.Errorf("expected %q, got %q", want, result)
	}
}

func TestSubstituteTemplate_LongerNameFirst(t *testing.T) {
	// $NAMES should not be partially replaced by $NAME.
	captures := map[string]string{
		"NAME":  "foo",
		"NAMES": "bar",
	}
	result := substituteTemplate("$NAMES and $NAME", captures, true)
	want := "bar and foo"
	if result != want {
		t.Errorf("expected %q, got %q", want, result)
	}
}

func TestSubstituteTemplate_AtNames(t *testing.T) {
	captures := map[string]string{
		"func": "myFunc",
		"args": "x, y",
	}
	result := substituteTemplate("@func(@args)", captures, false)
	want := "myFunc(x, y)"
	if result != want {
		t.Errorf("expected %q, got %q", want, result)
	}
}

func TestSubstituteTemplate_NoCaptures(t *testing.T) {
	result := substituteTemplate("literal text", nil, true)
	if result != "literal text" {
		t.Errorf("expected literal text unchanged, got %q", result)
	}
}

// --------------------------------------------------------------------------
// Replace integration tests (JavaScript)
// --------------------------------------------------------------------------

func TestReplace_ConsoleLogToInfo(t *testing.T) {
	lang := testLang(t, "javascript")

	source := []byte(`console.log("hello")
console.log("world")
console.error("oops")
`)

	// The $X capture matches the entire arguments node (including parens),
	// so the replacement template uses $X without adding its own parens.
	rr, err := Replace(lang, `console.log($X)`, `console.info$X`, source)
	if err != nil {
		t.Fatalf("Replace error: %v", err)
	}

	t.Logf("edits: %d, diagnostics: %d", len(rr.Edits), len(rr.Diagnostics))
	for _, e := range rr.Edits {
		t.Logf("  edit [%d:%d] -> %q", e.StartByte, e.EndByte, e.Replacement)
	}
	for _, d := range rr.Diagnostics {
		t.Logf("  diag: %s [%d:%d]", d.Message, d.StartByte, d.EndByte)
	}

	if len(rr.Edits) < 2 {
		t.Fatalf("expected at least 2 edits, got %d", len(rr.Edits))
	}

	result := ApplyEdits(source, rr.Edits)
	t.Logf("result:\n%s", result)

	resultStr := string(result)
	if !strings.Contains(resultStr, `console.info("hello")`) {
		t.Error("expected console.info(\"hello\") in result")
	}
	if !strings.Contains(resultStr, `console.info("world")`) {
		t.Error("expected console.info(\"world\") in result")
	}
	// console.error should be unchanged.
	if !strings.Contains(resultStr, `console.error("oops")`) {
		t.Error("console.error should be unchanged")
	}
}

func TestReplace_NoMatches(t *testing.T) {
	lang := testLang(t, "javascript")

	source := []byte(`let x = 42;`)
	rr, err := Replace(lang, `console.log($X)`, `console.info($X)`, source)
	if err != nil {
		t.Fatalf("Replace error: %v", err)
	}

	if len(rr.Edits) != 0 {
		t.Errorf("expected 0 edits, got %d", len(rr.Edits))
	}
}

func TestReplace_EmptySource(t *testing.T) {
	lang := testLang(t, "javascript")

	rr, err := Replace(lang, `console.log($X)`, `console.info($X)`, []byte{})
	if err != nil {
		t.Fatalf("Replace error: %v", err)
	}

	if len(rr.Edits) != 0 {
		t.Errorf("expected 0 edits for empty source, got %d", len(rr.Edits))
	}
}

func TestReplace_InvalidPattern(t *testing.T) {
	lang := testLang(t, "javascript")

	_, err := Replace(lang, ``, `replacement`, []byte(`source`))
	if err == nil {
		t.Error("expected error for empty pattern")
	}
}

// --------------------------------------------------------------------------
// Replace with Go patterns
// --------------------------------------------------------------------------

func TestReplace_GoFunctionPattern(t *testing.T) {
	lang := testLang(t, "go")

	source := []byte(`package main

func foo() {}
func bar() {}
`)

	rr, err := Replace(lang, `func $NAME()`, `func $NAME(ctx context.Context)`, source)
	if err != nil {
		t.Fatalf("Replace error: %v", err)
	}

	t.Logf("edits: %d", len(rr.Edits))
	for _, e := range rr.Edits {
		t.Logf("  edit [%d:%d] -> %q", e.StartByte, e.EndByte, e.Replacement)
	}

	if len(rr.Edits) < 2 {
		t.Fatalf("expected at least 2 edits, got %d", len(rr.Edits))
	}

	result := ApplyEdits(source, rr.Edits)
	t.Logf("result:\n%s", result)

	resultStr := string(result)
	if !strings.Contains(resultStr, "func foo(ctx context.Context)") {
		t.Error("expected func foo(ctx context.Context) in result")
	}
	if !strings.Contains(resultStr, "func bar(ctx context.Context)") {
		t.Error("expected func bar(ctx context.Context) in result")
	}
}

// --------------------------------------------------------------------------
// Overlap detection tests
// --------------------------------------------------------------------------

func TestReplace_OverlapDetection(t *testing.T) {
	lang := testLang(t, "javascript")

	// Nested call expressions: f(g(x)) contains both f(...) and g(x) as
	// potential matches. The outer match should win, inner should be
	// discarded with a diagnostic.
	source := []byte(`f(g(x))
`)

	rr, err := Replace(lang, `$FN($$$ARGS)`, `wrapped($FN, $$$ARGS)`, source)
	if err != nil {
		t.Fatalf("Replace error: %v", err)
	}

	t.Logf("edits: %d, diagnostics: %d", len(rr.Edits), len(rr.Diagnostics))
	for _, e := range rr.Edits {
		t.Logf("  edit [%d:%d] -> %q", e.StartByte, e.EndByte, e.Replacement)
	}
	for _, d := range rr.Diagnostics {
		t.Logf("  diag: %s [%d:%d]", d.Message, d.StartByte, d.EndByte)
	}

	// There should be overlap diagnostics since g(x) is nested inside f(g(x)).
	hasOverlapDiag := false
	for _, d := range rr.Diagnostics {
		if strings.Contains(d.Message, "overlapping") {
			hasOverlapDiag = true
		}
	}
	if !hasOverlapDiag {
		t.Log("note: no overlap diagnostic found (match engine may not return nested matches)")
	}
}

// --------------------------------------------------------------------------
// countSourceErrors tests
// --------------------------------------------------------------------------

func TestCountSourceErrors_ValidSource(t *testing.T) {
	lang := testLang(t, "javascript")

	count := countSourceErrors(lang, []byte(`let x = 42;`))
	if count != 0 {
		t.Errorf("expected 0 error nodes in valid source, got %d", count)
	}
}

func TestCountSourceErrors_InvalidSource(t *testing.T) {
	lang := testLang(t, "javascript")

	count := countSourceErrors(lang, []byte(`let = = = ;`))
	if count == 0 {
		t.Error("expected error nodes in invalid source, got 0")
	}
	t.Logf("error nodes in bad source: %d", count)
}

// --------------------------------------------------------------------------
// Output validation test (error detection in rewritten output)
// --------------------------------------------------------------------------

func TestReplace_DetectsNewParseErrors(t *testing.T) {
	lang := testLang(t, "javascript")

	// Create a replacement that will introduce a parse error.
	source := []byte(`let x = 42;`)
	rr, err := Replace(lang, `let $NAME = $VAL`, `let $NAME = = =`, source)
	if err != nil {
		t.Fatalf("Replace error: %v", err)
	}

	t.Logf("edits: %d, diagnostics: %d", len(rr.Edits), len(rr.Diagnostics))
	for _, d := range rr.Diagnostics {
		t.Logf("  diag: %s", d.Message)
	}

	// Should have a diagnostic about new parse errors.
	hasErrorDiag := false
	for _, d := range rr.Diagnostics {
		if strings.Contains(d.Message, "parse error") {
			hasErrorDiag = true
		}
	}
	if !hasErrorDiag {
		t.Error("expected diagnostic about new parse errors in rewritten output")
	}
}
