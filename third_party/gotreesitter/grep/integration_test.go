package grep

import (
	"strings"
	"testing"
)

// Integration tests exercise the full RunQuery pipeline (parse -> match ->
// where filter -> replace) against realistic multi-function source code in
// multiple languages.

// --------------------------------------------------------------------------
// Test 1: Go error handling — find functions returning error
// --------------------------------------------------------------------------

func TestIntegration_GoErrorHandling(t *testing.T) {
	source := []byte(`package service

import (
	"database/sql"
	"fmt"
	"net/http"
)

func FetchUser(db *sql.DB, id int) (*User, error) {
	row := db.QueryRow("SELECT * FROM users WHERE id = ?", id)
	var u User
	if err := row.Scan(&u.Name, &u.Email); err != nil {
		return nil, fmt.Errorf("fetch user %d: %w", id, err)
	}
	return &u, nil
}

func HandleRequest(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

func ValidateInput(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty input")
	}
	return nil
}

func ComputeHash(data []byte) string {
	return "abc123"
}

func SaveRecord(db *sql.DB, record Record) error {
	_, err := db.Exec("INSERT INTO records VALUES (?)", record.ID)
	return err
}
`)

	qr, err := RunQuery(
		`find go::func $NAME($$$PARAMS) error`,
		source, defaultResolver,
	)
	if err != nil {
		t.Fatalf("RunQuery error: %v", err)
	}

	// Should match: ValidateInput, SaveRecord (functions with params returning
	// bare error). FetchUser returns (*User, error) which is a result_types
	// node, not a bare "error".
	if len(qr.Matches) < 2 {
		t.Fatalf("expected at least 2 matches, got %d", len(qr.Matches))
	}

	names := make(map[string]bool)
	for _, m := range qr.Matches {
		cap, ok := m.Captures["NAME"]
		if !ok {
			t.Fatal("missing NAME capture")
		}
		names[string(cap.Text)] = true
		t.Logf("matched: %s", string(cap.Text))
	}

	if !names["ValidateInput"] {
		t.Error("expected ValidateInput in matches")
	}
	if !names["SaveRecord"] {
		t.Error("expected SaveRecord in matches")
	}
	// HandleRequest and ComputeHash should NOT match (wrong return type).
	if names["HandleRequest"] {
		t.Error("HandleRequest should not match (no error return)")
	}
	if names["ComputeHash"] {
		t.Error("ComputeHash should not match (returns string)")
	}
}

// --------------------------------------------------------------------------
// Test 2: Go where-clause — filter by name pattern
// --------------------------------------------------------------------------

func TestIntegration_GoWhereClause(t *testing.T) {
	source := []byte(`package handlers

func TestCreateUser(t *testing.T) {
	user := createUser()
	assert(t, user != nil)
}

func TestDeleteUser(t *testing.T) {
	err := deleteUser(1)
	assert(t, err == nil)
}

func TestUpdateUser(t *testing.T) {
	err := updateUser(1, "new")
	assert(t, err == nil)
}

func BenchmarkCreateUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		createUser()
	}
}

func helperSetup() {
	initDB()
}

func ExampleCreateUser() {
	createUser()
}
`)

	qr, err := RunQuery(
		`find go::func $NAME($$$PARAMS) where { matches($NAME, "^Test") }`,
		source, defaultResolver,
	)
	if err != nil {
		t.Fatalf("RunQuery error: %v", err)
	}

	if len(qr.Matches) != 3 {
		t.Fatalf("expected 3 Test* matches, got %d", len(qr.Matches))
	}

	names := make(map[string]bool)
	for _, m := range qr.Matches {
		name := string(m.Captures["NAME"].Text)
		names[name] = true
		t.Logf("matched: %s", name)

		if !strings.HasPrefix(name, "Test") {
			t.Errorf("where clause should have filtered non-Test function: %s", name)
		}
	}

	if !names["TestCreateUser"] {
		t.Error("expected TestCreateUser")
	}
	if !names["TestDeleteUser"] {
		t.Error("expected TestDeleteUser")
	}
	if !names["TestUpdateUser"] {
		t.Error("expected TestUpdateUser")
	}
	if names["BenchmarkCreateUser"] {
		t.Error("BenchmarkCreateUser should be filtered out")
	}
	if names["helperSetup"] {
		t.Error("helperSetup should be filtered out")
	}
}

// --------------------------------------------------------------------------
// Test 3: JavaScript rewrite — console.log to console.info
// --------------------------------------------------------------------------

func TestIntegration_JSConsoleRewrite(t *testing.T) {
	source := []byte(`function initialize() {
  console.log("starting app");
  const config = loadConfig();
  console.log("config loaded:", config.name);
  console.error("this should stay");
  console.warn("this too");
  return config;
}

function shutdown() {
  console.log("shutting down");
  cleanup();
  console.log("done");
}
`)

	qr, err := RunQuery(
		`find javascript::console.log($ARG) replace { console.info$ARG }`,
		source, defaultResolver,
	)
	if err != nil {
		t.Fatalf("RunQuery error: %v", err)
	}

	if len(qr.Matches) < 4 {
		t.Fatalf("expected at least 4 console.log matches, got %d", len(qr.Matches))
	}

	if qr.ReplaceResult == nil {
		t.Fatal("expected non-nil ReplaceResult")
	}

	result := ApplyEdits(source, qr.ReplaceResult.Edits)
	resultStr := string(result)
	t.Logf("rewritten:\n%s", resultStr)

	// All console.log calls should be rewritten.
	if strings.Contains(resultStr, "console.log") {
		t.Error("expected all console.log calls to be rewritten")
	}

	// console.info should appear in the result.
	if !strings.Contains(resultStr, "console.info") {
		t.Error("expected console.info in rewritten output")
	}

	// console.error and console.warn should be untouched.
	if !strings.Contains(resultStr, "console.error") {
		t.Error("console.error should be unchanged")
	}
	if !strings.Contains(resultStr, "console.warn") {
		t.Error("console.warn should be unchanged")
	}
}

// --------------------------------------------------------------------------
// Test 4: JavaScript require-to-import rewrite
// --------------------------------------------------------------------------

func TestIntegration_JSRequireToImport(t *testing.T) {
	source := []byte(`const fs = require("fs");
const path = require("path");
const http = require("http");
const myLib = require("./lib/myLib");
`)

	// In this engine, $MOD captures the arguments node including parens,
	// e.g. ("fs"). The replacement template uses $MOD directly so the
	// parens carry over: require("fs") -> import("fs").
	qr, err := RunQuery(
		`find javascript::require($MOD) replace { import$MOD }`,
		source, defaultResolver,
	)
	if err != nil {
		t.Fatalf("RunQuery error: %v", err)
	}

	if len(qr.Matches) < 4 {
		t.Fatalf("expected at least 4 require() matches, got %d", len(qr.Matches))
	}

	// Verify all four require calls were matched by checking MOD captures.
	// $MOD captures the arguments node which includes parens, e.g. ("fs").
	for _, m := range qr.Matches {
		cap, ok := m.Captures["MOD"]
		if !ok {
			t.Fatal("missing MOD capture")
		}
		modText := string(cap.Text)
		t.Logf("captured MOD: %s", modText)
		// Each capture should contain a quoted module string.
		if !strings.Contains(modText, `"`) {
			t.Errorf("expected quoted string in MOD capture, got %q", modText)
		}
	}

	if qr.ReplaceResult == nil {
		t.Fatal("expected non-nil ReplaceResult")
	}

	result := ApplyEdits(source, qr.ReplaceResult.Edits)
	resultStr := string(result)
	t.Logf("rewritten:\n%s", resultStr)

	// Verify rewrites occurred.
	if strings.Contains(resultStr, "require(") {
		t.Error("expected all require() calls to be rewritten")
	}
	if !strings.Contains(resultStr, "import(") {
		t.Error("expected import() in rewritten output")
	}
}

// --------------------------------------------------------------------------
// Test 5: Python functions — def $NAME($$$PARAMS): $$$BODY
// --------------------------------------------------------------------------

func TestIntegration_PythonFunctions(t *testing.T) {
	lang := testLang(t, "python")

	source := []byte(`class UserService:
    def create_user(self, name, email):
        user = User(name=name, email=email)
        self.db.save(user)
        return user

    def delete_user(self, user_id):
        self.db.delete(user_id)

    def get_user(self, user_id):
        return self.db.find(user_id)

def validate_email(email):
    return "@" in email

def process_batch(items, callback):
    for item in items:
        callback(item)

def main():
    svc = UserService()
    svc.create_user("alice", "alice@example.com")
`)

	// Python function definitions require a body to parse correctly.
	// The pattern `def $NAME($$$PARAMS): $$$BODY` compiles to a proper
	// function_definition S-expression query.
	results, err := Match(lang, `def $NAME($$$PARAMS): $$$BODY`, source)
	if err != nil {
		t.Fatalf("match error: %v", err)
	}

	if len(results) < 5 {
		t.Fatalf("expected at least 5 def matches (3 methods + 3 functions), got %d", len(results))
	}

	names := make(map[string]bool)
	for _, r := range results {
		cap, ok := r.Captures["NAME"]
		if !ok {
			t.Fatal("missing NAME capture")
		}
		name := string(cap.Text)
		names[name] = true
		t.Logf("matched: %s", name)
	}

	// Verify specific functions are found.
	expected := []string{"create_user", "delete_user", "get_user", "validate_email", "process_batch", "main"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected %s in matches", name)
		}
	}
}

// --------------------------------------------------------------------------
// Test 6: Rust function declarations and structural match via S-expression
// --------------------------------------------------------------------------

func TestIntegration_RustFunctionsAndSexp(t *testing.T) {
	lang := testLang(t, "rust")

	source := []byte(`fn process_data() {
    let config = read_config().unwrap();
    let data = fetch_data(&config).unwrap();
    let parsed = parse_json(&data).unwrap();
    let validated = validate(parsed).expect("already checked");
    save_results(validated);
}

fn handle_error(err: String) {
    eprintln!("Error: {}", err);
}

fn compute(x: i32, y: i32) -> i32 {
    x + y
}
`)

	// Test 6a: Match function declarations using code pattern.
	// Rust requires a body to parse correctly as a function_item.
	results, err := Match(lang, `fn $NAME($$$PARAMS) { $$$BODY }`, source)
	if err != nil {
		t.Fatalf("Match error: %v", err)
	}

	if len(results) < 3 {
		t.Fatalf("expected at least 3 fn matches, got %d", len(results))
	}

	names := make(map[string]bool)
	for _, r := range results {
		cap, ok := r.Captures["NAME"]
		if !ok {
			t.Fatal("missing NAME capture")
		}
		names[string(cap.Text)] = true
		t.Logf("matched fn: %s", string(cap.Text))
	}

	for _, expected := range []string{"process_data", "handle_error", "compute"} {
		if !names[expected] {
			t.Errorf("expected %s in matches", expected)
		}
	}

	// Test 6b: Use MatchSexp to find method calls matching .unwrap().
	// The code pattern compiler cannot handle `$EXPR.unwrap()` as a
	// standalone Rust expression, so we use an S-expression query.
	sexp := `(call_expression
		function: (field_expression
			value: (_) @expr
			field: (field_identifier) @method
		)
		arguments: (arguments) @args
	)`
	sexpResults, err := MatchSexp(lang, sexp, source)
	if err != nil {
		t.Fatalf("MatchSexp error: %v", err)
	}

	// Filter to only .unwrap() calls.
	var unwrapExprs []string
	for _, r := range sexpResults {
		methodCap, ok := r.Captures["method"]
		if !ok {
			continue
		}
		if string(methodCap.Text) == "unwrap" {
			exprCap := r.Captures["expr"]
			unwrapExprs = append(unwrapExprs, string(exprCap.Text))
			t.Logf("found .unwrap() on: %s", string(exprCap.Text))
		}
	}

	if len(unwrapExprs) != 3 {
		t.Fatalf("expected 3 .unwrap() calls, got %d", len(unwrapExprs))
	}

	// Verify the specific expressions that call .unwrap().
	unwrapSet := make(map[string]bool)
	for _, e := range unwrapExprs {
		unwrapSet[e] = true
	}
	if !unwrapSet["read_config()"] {
		t.Error("expected read_config().unwrap()")
	}
	if !unwrapSet["fetch_data(&config)"] {
		t.Error("expected fetch_data(&config).unwrap()")
	}
	if !unwrapSet["parse_json(&data)"] {
		t.Error("expected parse_json(&data).unwrap()")
	}
}

// --------------------------------------------------------------------------
// Test 7: Full pipeline — find + where + replace
// --------------------------------------------------------------------------

func TestIntegration_FullPipelineFindWhereReplace(t *testing.T) {
	source := []byte(`package main

func TestAdd() {}
func TestSub() {}
func TestMul() {}
func BenchmarkAdd() {}
func helperSetup() {}
`)

	qr, err := RunQuery(
		`find go::func $NAME() where { matches($NAME, "^Test") } replace { func $NAME(t *testing.T) }`,
		source, defaultResolver,
	)
	if err != nil {
		t.Fatalf("RunQuery error: %v", err)
	}

	// Where clause should filter to 3 Test* functions.
	if len(qr.Matches) != 3 {
		t.Fatalf("expected 3 matches after where filter, got %d", len(qr.Matches))
	}

	// Verify only Test functions survived filtering.
	for _, m := range qr.Matches {
		name := string(m.Captures["NAME"].Text)
		if !strings.HasPrefix(name, "Test") {
			t.Errorf("unexpected match after where filter: %s", name)
		}
	}

	// Replace clause should have produced edits.
	if qr.ReplaceResult == nil {
		t.Fatal("expected non-nil ReplaceResult")
	}
	if len(qr.ReplaceResult.Edits) != 3 {
		t.Fatalf("expected 3 edits, got %d", len(qr.ReplaceResult.Edits))
	}

	result := ApplyEdits(source, qr.ReplaceResult.Edits)
	resultStr := string(result)
	t.Logf("rewritten:\n%s", resultStr)

	// Verify the rewritten test functions have the t parameter.
	if !strings.Contains(resultStr, "func TestAdd(t *testing.T)") {
		t.Error("expected TestAdd to have t *testing.T parameter")
	}
	if !strings.Contains(resultStr, "func TestSub(t *testing.T)") {
		t.Error("expected TestSub to have t *testing.T parameter")
	}
	if !strings.Contains(resultStr, "func TestMul(t *testing.T)") {
		t.Error("expected TestMul to have t *testing.T parameter")
	}

	// BenchmarkAdd and helperSetup should be untouched.
	if !strings.Contains(resultStr, "func BenchmarkAdd()") {
		t.Error("BenchmarkAdd should be unchanged")
	}
	if !strings.Contains(resultStr, "func helperSetup()") {
		t.Error("helperSetup should be unchanged")
	}
}

// --------------------------------------------------------------------------
// Test 8: Cross-language consistency — function declarations
// --------------------------------------------------------------------------

func TestIntegration_CrossLanguageFunctionDecls(t *testing.T) {
	type langCase struct {
		name    string
		pattern string
		source  []byte
		expect  []string // expected function names
	}

	cases := []langCase{
		{
			name:    "go",
			pattern: `func $NAME($$$PARAMS)`,
			source: []byte(`package main

func alpha(x int) {}
func beta(a, b string) {}
func gamma() {}
`),
			expect: []string{"alpha", "beta", "gamma"},
		},
		{
			name:    "javascript",
			pattern: `function $NAME($$$PARAMS) { $$$BODY }`,
			source: []byte(`function alpha(x) { return x; }
function beta(a, b) { return a + b; }
function gamma() { return 42; }
const delta = () => 99;
`),
			expect: []string{"alpha", "beta", "gamma"},
		},
		{
			name:    "python",
			pattern: `def $NAME($$$PARAMS): $$$BODY`,
			source: []byte(`def alpha(x):
    return x

def beta(a, b):
    return a + b

def gamma():
    return 42
`),
			expect: []string{"alpha", "beta", "gamma"},
		},
		{
			name:    "rust",
			pattern: `fn $NAME($$$PARAMS) { $$$BODY }`,
			source: []byte(`fn alpha(x: i32) -> i32 {
    x
}

fn beta(a: i32, b: i32) -> i32 {
    a + b
}

fn gamma() -> i32 {
    42
}
`),
			expect: []string{"alpha", "beta", "gamma"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lang := testLang(t, tc.name)

			results, err := Match(lang, tc.pattern, tc.source)
			if err != nil {
				t.Fatalf("match error for %s: %v", tc.name, err)
			}

			if len(results) < len(tc.expect) {
				t.Fatalf("%s: expected at least %d matches, got %d",
					tc.name, len(tc.expect), len(results))
			}

			names := make(map[string]bool)
			for _, r := range results {
				cap, ok := r.Captures["NAME"]
				if !ok {
					t.Fatalf("%s: missing NAME capture", tc.name)
				}
				name := string(cap.Text)
				names[name] = true
				t.Logf("%s: matched function %s", tc.name, name)
			}

			for _, expected := range tc.expect {
				if !names[expected] {
					t.Errorf("%s: expected function %s in matches", tc.name, expected)
				}
			}
		})
	}
}
