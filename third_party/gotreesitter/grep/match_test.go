package grep

import (
	"testing"
)

// --------------------------------------------------------------------------
// Match — Go patterns
// --------------------------------------------------------------------------

func TestMatch_GoFunctionDecl(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func myFunc() {}
func another() {}
func withParam(x int) {}
`)

	results, err := Match(lang, `func $NAME()`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	// Should match myFunc and another (empty params), not withParam.
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	names := make(map[string]bool)
	for _, r := range results {
		cap, ok := r.Captures["NAME"]
		if !ok {
			t.Fatal("missing NAME capture")
		}
		names[string(cap.Text)] = true
	}
	if !names["myFunc"] {
		t.Error("expected myFunc to be captured")
	}
	if !names["another"] {
		t.Error("expected another to be captured")
	}
}

func TestMatch_GoFunctionWithErrorReturn(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func good() error { return nil }
func bad(x int) error { return nil }
func ugly() int { return 0 }
`)

	results, err := Match(lang, `func $NAME() error`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	// Only "good" should match (empty params AND error return).
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if name := string(results[0].Captures["NAME"].Text); name != "good" {
		t.Errorf("expected 'good', got %q", name)
	}
}

func TestMatch_GoFunctionWithVariadicParams(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func handleRequest(w http.ResponseWriter, r *http.Request) error {
	return nil
}
func process(data []byte) error {
	return nil
}
func helper() string {
	return ""
}
`)

	results, err := Match(lang, `func $NAME($$$PARAMS) error`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	nameSet := make(map[string]bool)
	for _, r := range results {
		cap, ok := r.Captures["NAME"]
		if !ok {
			t.Fatal("missing NAME capture")
		}
		nameSet[string(cap.Text)] = true
	}
	if !nameSet["handleRequest"] {
		t.Error("expected handleRequest to be captured")
	}
	if !nameSet["process"] {
		t.Error("expected process to be captured")
	}
}

func TestMatch_GoBinaryExpression(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func f() {
	a := x + y
	b := 1 + 2
	c := a - b
}
`)

	results, err := Match(lang, `$X + $Y`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	// Should match "x + y" and "1 + 2" but NOT "a - b".
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, r := range results {
		xCap, ok := r.Captures["X"]
		if !ok {
			t.Fatal("missing X capture")
		}
		yCap, ok := r.Captures["Y"]
		if !ok {
			t.Fatal("missing Y capture")
		}
		t.Logf("X=%q Y=%q", string(xCap.Text), string(yCap.Text))
	}
}

func TestMatch_GoReturnStatement(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func f() error {
	if false {
		return nil
	}
	return fmt.Errorf("oops")
}
`)

	results, err := Match(lang, `return $ERR`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	for _, r := range results {
		cap, ok := r.Captures["ERR"]
		if !ok {
			t.Fatal("missing ERR capture")
		}
		t.Logf("ERR=%q", string(cap.Text))
	}
}

// --------------------------------------------------------------------------
// Match — byte range correctness
// --------------------------------------------------------------------------

func TestMatch_ByteRangesCorrect(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func hello() {}
func world() {}
`)

	results, err := Match(lang, `func $NAME()`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	for _, r := range results {
		if r.StartByte >= r.EndByte {
			t.Errorf("invalid byte range: start=%d end=%d", r.StartByte, r.EndByte)
		}

		cap := r.Captures["NAME"]
		if cap.StartByte >= cap.EndByte {
			t.Errorf("invalid capture byte range: start=%d end=%d", cap.StartByte, cap.EndByte)
		}

		// Verify capture text matches what source bytes say.
		capText := string(source[cap.StartByte:cap.EndByte])
		if capText != string(cap.Text) {
			t.Errorf("capture text mismatch: source[%d:%d]=%q, cap.Text=%q",
				cap.StartByte, cap.EndByte, capText, string(cap.Text))
		}
	}
}

// --------------------------------------------------------------------------
// Match — no results
// --------------------------------------------------------------------------

func TestMatch_NoResults(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func helper() string { return "" }
`)

	results, err := Match(lang, `func $NAME() error`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	// Should return empty slice, not nil.
	if results == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// --------------------------------------------------------------------------
// Match — error cases
// --------------------------------------------------------------------------

func TestMatch_NilLanguage(t *testing.T) {
	_, err := Match(nil, `func $NAME()`, []byte(`package main`))
	if err == nil {
		t.Fatal("expected error for nil language")
	}
}

func TestMatch_EmptyPattern(t *testing.T) {
	lang := testLang(t, "go")
	_, err := Match(lang, "", []byte(`package main`))
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

// --------------------------------------------------------------------------
// MatchSexp — raw S-expression queries
// --------------------------------------------------------------------------

func TestMatchSexp_GoFunctionDecl(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func myFunc() {}
func another() {}
func withParam(x int) {}
`)

	// Match all function declarations and capture their name.
	sexp := `(function_declaration name: (identifier) @name)`
	results, err := MatchSexp(lang, sexp, source)
	if err != nil {
		t.Fatalf("matchsexp error: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results (all functions), got %d", len(results))
	}

	names := make(map[string]bool)
	for _, r := range results {
		cap, ok := r.Captures["name"]
		if !ok {
			t.Fatal("missing name capture")
		}
		names[string(cap.Text)] = true
	}
	if !names["myFunc"] || !names["another"] || !names["withParam"] {
		t.Errorf("expected all three function names, got %v", names)
	}
}

func TestMatchSexp_NilLanguage(t *testing.T) {
	_, err := MatchSexp(nil, `(identifier)`, []byte(`package main`))
	if err == nil {
		t.Fatal("expected error for nil language")
	}
}

func TestMatchSexp_EmptyQuery(t *testing.T) {
	lang := testLang(t, "go")
	_, err := MatchSexp(lang, "  ", []byte(`package main`))
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestMatchSexp_NoResults(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

var x = 1
`)

	// No function declarations in this source.
	results, err := MatchSexp(lang, `(function_declaration name: (identifier) @name)`, source)
	if err != nil {
		t.Fatalf("matchsexp error: %v", err)
	}
	if results == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// --------------------------------------------------------------------------
// Compile + CompiledPattern.Match — reusable compilation
// --------------------------------------------------------------------------

func TestCompile_ReuseAcrossFiles(t *testing.T) {
	lang := testLang(t, "go")

	cp, err := Compile(lang, `func $NAME() error`)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}

	// File 1: one matching function.
	source1 := []byte(`package main

func run() error { return nil }
func help() string { return "" }
`)
	results1, err := cp.Match(source1)
	if err != nil {
		t.Fatalf("match error on file 1: %v", err)
	}
	if len(results1) != 1 {
		t.Fatalf("file 1: expected 1 result, got %d", len(results1))
	}
	if name := string(results1[0].Captures["NAME"].Text); name != "run" {
		t.Errorf("file 1: expected 'run', got %q", name)
	}

	// File 2: two matching functions.
	source2 := []byte(`package util

func open() error { return nil }
func close() error { return nil }
func read(p []byte) (int, error) { return 0, nil }
`)
	results2, err := cp.Match(source2)
	if err != nil {
		t.Fatalf("match error on file 2: %v", err)
	}
	if len(results2) != 2 {
		t.Fatalf("file 2: expected 2 results, got %d", len(results2))
	}

	nameSet := make(map[string]bool)
	for _, r := range results2 {
		nameSet[string(r.Captures["NAME"].Text)] = true
	}
	if !nameSet["open"] || !nameSet["close"] {
		t.Errorf("file 2: expected open and close, got %v", nameSet)
	}

	// File 3: no matching functions.
	source3 := []byte(`package empty

func noop() {}
`)
	results3, err := cp.Match(source3)
	if err != nil {
		t.Fatalf("match error on file 3: %v", err)
	}
	if len(results3) != 0 {
		t.Errorf("file 3: expected 0 results, got %d", len(results3))
	}
}

func TestCompile_NilLanguage(t *testing.T) {
	_, err := Compile(nil, `func $NAME()`)
	if err == nil {
		t.Fatal("expected error for nil language")
	}
}

func TestCompile_EmptyPattern(t *testing.T) {
	lang := testLang(t, "go")
	_, err := Compile(lang, "")
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

// --------------------------------------------------------------------------
// Match — JavaScript patterns (cross-language validation)
// --------------------------------------------------------------------------

func TestMatch_JSFunctionDecl(t *testing.T) {
	lang := testLang(t, "javascript")
	source := []byte(`
function hello() { return 1; }
function world(x) { return x; }
const arrow = () => 42;
`)

	results, err := Match(lang, `function $NAME($$$PARAMS) { $$$BODY }`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	for _, r := range results {
		cap, ok := r.Captures["NAME"]
		if !ok {
			t.Fatal("missing NAME capture")
		}
		t.Logf("JS function: %s", string(cap.Text))
	}
}

func TestMatch_JSCallExpression(t *testing.T) {
	lang := testLang(t, "javascript")
	source := []byte(`
console.log("hello");
console.log(42);
console.error("oops");
`)

	results, err := Match(lang, `console.log($ARG)`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, r := range results {
		cap, ok := r.Captures["ARG"]
		if !ok {
			t.Fatal("missing ARG capture")
		}
		t.Logf("console.log arg: %s", string(cap.Text))
	}
}

// --------------------------------------------------------------------------
// Match — captures contain Node references
// --------------------------------------------------------------------------

func TestMatch_CapturesHaveNodes(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func hello() {}
`)

	results, err := Match(lang, `func $NAME()`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	cap := results[0].Captures["NAME"]
	if cap.Node == nil {
		t.Fatal("expected non-nil Node in capture")
	}
}

// --------------------------------------------------------------------------
// Match — wildcard captures are excluded from results
// --------------------------------------------------------------------------

func TestMatch_WildcardNotCaptured(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func myFunc() {}
func another() {}
`)

	results, err := Match(lang, `func $_()`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// Wildcard ($_ ) should not produce named captures.
	for _, r := range results {
		if len(r.Captures) != 0 {
			t.Errorf("expected no captures for wildcard pattern, got %d", len(r.Captures))
		}
	}
}
