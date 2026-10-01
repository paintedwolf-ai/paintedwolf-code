package grep

import (
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// defaultResolver builds a LangResolver using the grammars registry.
func defaultResolver(name string) *gotreesitter.Language {
	entry := grammars.DetectLanguageByName(name)
	if entry == nil {
		return nil
	}
	return entry.Language()
}

// --------------------------------------------------------------------------
// RunQuery — basic match (no where, no replace)
// --------------------------------------------------------------------------

func TestRunQuery_GoFunctionDecl(t *testing.T) {
	source := []byte(`package main

func hello() {}
func world() {}
func withParam(x int) {}
`)

	qr, err := RunQuery(`find go::func $NAME()`, source, defaultResolver)
	if err != nil {
		t.Fatalf("RunQuery error: %v", err)
	}

	if len(qr.Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(qr.Matches))
	}

	names := make(map[string]bool)
	for _, m := range qr.Matches {
		cap, ok := m.Captures["NAME"]
		if !ok {
			t.Fatal("missing NAME capture")
		}
		names[string(cap.Text)] = true
	}
	if !names["hello"] {
		t.Error("expected hello in matches")
	}
	if !names["world"] {
		t.Error("expected world in matches")
	}

	if qr.ReplaceResult != nil {
		t.Error("expected nil ReplaceResult when no replace clause")
	}
}

// --------------------------------------------------------------------------
// RunQuery — with where clause
// --------------------------------------------------------------------------

func TestRunQuery_GoFuncWithWhere(t *testing.T) {
	source := []byte(`package main

func TestFoo() {}
func TestBar() {}
func helperFunc() {}
`)

	qr, err := RunQuery(
		`find go::func $NAME() where { matches($NAME, "^Test") }`,
		source, defaultResolver,
	)
	if err != nil {
		t.Fatalf("RunQuery error: %v", err)
	}

	if len(qr.Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(qr.Matches))
	}

	for _, m := range qr.Matches {
		name := string(m.Captures["NAME"].Text)
		if !strings.HasPrefix(name, "Test") {
			t.Errorf("expected Test* function, got %q", name)
		}
	}
}

func TestRunQuery_GoFuncWhereNotContains(t *testing.T) {
	source := []byte(`package main

func processCtx() {
	ctx.Err()
}

func processSimple() {
	fmt.Println("done")
}
`)

	qr, err := RunQuery(
		`find go::func $NAME() where { not contains($NAME, "Ctx") }`,
		source, defaultResolver,
	)
	if err != nil {
		t.Fatalf("RunQuery error: %v", err)
	}

	if len(qr.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(qr.Matches))
	}

	name := string(qr.Matches[0].Captures["NAME"].Text)
	if name != "processSimple" {
		t.Errorf("expected processSimple, got %q", name)
	}
}

// --------------------------------------------------------------------------
// RunQuery — with replace clause (no where)
// --------------------------------------------------------------------------

func TestRunQuery_JSConsoleReplace(t *testing.T) {
	source := []byte(`console.log("hello")
console.log("world")
console.error("oops")
`)

	qr, err := RunQuery(
		`find javascript::console.log($ARG) replace { console.info$ARG }`,
		source, defaultResolver,
	)
	if err != nil {
		t.Fatalf("RunQuery error: %v", err)
	}

	if len(qr.Matches) < 2 {
		t.Fatalf("expected at least 2 matches, got %d", len(qr.Matches))
	}

	if qr.ReplaceResult == nil {
		t.Fatal("expected non-nil ReplaceResult")
	}

	if len(qr.ReplaceResult.Edits) < 2 {
		t.Fatalf("expected at least 2 edits, got %d", len(qr.ReplaceResult.Edits))
	}

	result := ApplyEdits(source, qr.ReplaceResult.Edits)
	resultStr := string(result)

	if !strings.Contains(resultStr, `console.info("hello")`) {
		t.Error("expected console.info(\"hello\") in result")
	}
	if !strings.Contains(resultStr, `console.info("world")`) {
		t.Error("expected console.info(\"world\") in result")
	}
	if !strings.Contains(resultStr, `console.error("oops")`) {
		t.Error("expected console.error unchanged")
	}
}

// --------------------------------------------------------------------------
// RunQueryWithLang — bare pattern
// --------------------------------------------------------------------------

func TestRunQueryWithLang_BarePattern(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func hello() {}
func world() {}
func withParam(x int) {}
`)

	qr, err := RunQueryWithLang(`func $NAME()`, source, lang)
	if err != nil {
		t.Fatalf("RunQueryWithLang error: %v", err)
	}

	if len(qr.Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(qr.Matches))
	}

	names := make(map[string]bool)
	for _, m := range qr.Matches {
		names[string(m.Captures["NAME"].Text)] = true
	}
	if !names["hello"] || !names["world"] {
		t.Errorf("expected hello and world, got %v", names)
	}
}

func TestRunQueryWithLang_WithWhereClause(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func TestAlpha() {}
func TestBeta() {}
func helper() {}
`)

	qr, err := RunQueryWithLang(
		`func $NAME() where { matches($NAME, "^Test") }`,
		source, lang,
	)
	if err != nil {
		t.Fatalf("RunQueryWithLang error: %v", err)
	}

	if len(qr.Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(qr.Matches))
	}

	for _, m := range qr.Matches {
		name := string(m.Captures["NAME"].Text)
		if !strings.HasPrefix(name, "Test") {
			t.Errorf("expected Test* function, got %q", name)
		}
	}
}

func TestRunQueryWithLang_WithLangPrefix(t *testing.T) {
	// When both a lang prefix in the query AND a lang arg are provided,
	// the provided lang arg takes precedence.
	lang := testLang(t, "go")
	source := []byte(`package main

func hello() {}
`)

	qr, err := RunQueryWithLang(`find go::func $NAME()`, source, lang)
	if err != nil {
		t.Fatalf("RunQueryWithLang error: %v", err)
	}

	if len(qr.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(qr.Matches))
	}
}

// --------------------------------------------------------------------------
// RunQuery — error cases
// --------------------------------------------------------------------------

func TestRunQuery_EmptyQuery(t *testing.T) {
	_, err := RunQuery("", nil, defaultResolver)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestRunQuery_UnknownLanguage(t *testing.T) {
	_, err := RunQuery(`find zzzunknown::$X`, nil, defaultResolver)
	if err == nil {
		t.Fatal("expected error for unknown language")
	}
}

func TestRunQuery_NoLanguage(t *testing.T) {
	_, err := RunQuery(`func $NAME()`, nil, defaultResolver)
	if err == nil {
		t.Fatal("expected error when no language specified in RunQuery")
	}
}

func TestRunQuery_SexpRequiresRunQueryWithLang(t *testing.T) {
	_, err := RunQuery(`find sexp::(function_declaration)`, nil, defaultResolver)
	if err == nil {
		t.Fatal("expected error for sexp mode in RunQuery")
	}
}

func TestRunQueryWithLang_NilLanguage(t *testing.T) {
	_, err := RunQueryWithLang(`func $NAME()`, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil language")
	}
}

func TestRunQuery_InvalidWhereClause(t *testing.T) {
	source := []byte(`package main

func hello() {}
`)
	_, err := RunQuery(
		`find go::func $NAME() where { badFunc($NAME) }`,
		source, defaultResolver,
	)
	if err == nil {
		t.Fatal("expected error for invalid where clause")
	}
}

// --------------------------------------------------------------------------
// RunQuery — where + matches filter on Go test functions
// --------------------------------------------------------------------------

func TestRunQuery_WhereMatchesTestPrefix(t *testing.T) {
	source := []byte(`package main

func TestAdd() {}
func TestSub() {}
func BenchmarkMul() {}
func helper() {}
`)

	qr, err := RunQuery(
		`find go::func $NAME() where { matches($NAME, "^Test") }`,
		source, defaultResolver,
	)
	if err != nil {
		t.Fatalf("RunQuery error: %v", err)
	}

	if len(qr.Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(qr.Matches))
	}

	names := make(map[string]bool)
	for _, m := range qr.Matches {
		names[string(m.Captures["NAME"].Text)] = true
	}
	if !names["TestAdd"] {
		t.Error("expected TestAdd in results")
	}
	if !names["TestSub"] {
		t.Error("expected TestSub in results")
	}
}

// --------------------------------------------------------------------------
// RunQuery — no matches returns empty result
// --------------------------------------------------------------------------

func TestRunQuery_NoMatches(t *testing.T) {
	source := []byte(`package main

var x = 1
`)

	qr, err := RunQuery(`find go::func $NAME()`, source, defaultResolver)
	if err != nil {
		t.Fatalf("RunQuery error: %v", err)
	}

	if len(qr.Matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(qr.Matches))
	}

	if qr.ReplaceResult != nil {
		t.Error("expected nil ReplaceResult when no matches")
	}
}

// --------------------------------------------------------------------------
// RunQuery — where filters out all matches
// --------------------------------------------------------------------------

func TestRunQuery_WhereFiltersAllMatches(t *testing.T) {
	source := []byte(`package main

func alpha() {}
func beta() {}
`)

	qr, err := RunQuery(
		`find go::func $NAME() where { matches($NAME, "^Test") }`,
		source, defaultResolver,
	)
	if err != nil {
		t.Fatalf("RunQuery error: %v", err)
	}

	if len(qr.Matches) != 0 {
		t.Errorf("expected 0 matches after where filter, got %d", len(qr.Matches))
	}
}

// --------------------------------------------------------------------------
// DefaultResolver
// --------------------------------------------------------------------------

func TestDefaultResolver_KnownLanguage(t *testing.T) {
	resolver := DefaultResolver()
	lang := resolver("go")
	if lang == nil {
		t.Skip("go language not available")
	}
}

func TestDefaultResolver_UnknownLanguage(t *testing.T) {
	resolver := DefaultResolver()
	lang := resolver("zzzunknown")
	if lang != nil {
		t.Error("expected nil for unknown language")
	}
}

// --------------------------------------------------------------------------
// RunQuery — where with contains on function bodies
// --------------------------------------------------------------------------

func TestRunQuery_WhereContainsName(t *testing.T) {
	lang := testLang(t, "go")
	source := []byte(`package main

func fetchData() error {
	return nil
}

func processData() error {
	return nil
}

func helperFunc() error {
	return nil
}
`)

	// Match functions with error return, filtering to those whose name
	// contains "Data".
	results, err := Match(lang, `func $NAME() error`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	// All three functions should match the pattern.
	if len(results) != 3 {
		t.Fatalf("expected 3 matches, got %d", len(results))
	}

	// Use where-clause to filter by name containing "Data".
	filter, err := CompileWhere(`contains($NAME, "Data")`)
	if err != nil {
		t.Fatalf("compile where error: %v", err)
	}

	var filtered []Result
	for i := range results {
		if filter(&results[i], source, lang) {
			filtered = append(filtered, results[i])
		}
	}

	if len(filtered) != 2 {
		t.Fatalf("expected 2 filtered results, got %d", len(filtered))
	}

	names := make(map[string]bool)
	for _, r := range filtered {
		names[string(r.Captures["NAME"].Text)] = true
	}
	if !names["fetchData"] {
		t.Error("expected fetchData in filtered results")
	}
	if !names["processData"] {
		t.Error("expected processData in filtered results")
	}
}
