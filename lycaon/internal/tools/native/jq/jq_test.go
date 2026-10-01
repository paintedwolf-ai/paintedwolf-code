package jq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

func testBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	return sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{{
		ID:    tools.DefaultToolProfileID,
		Tools: map[string]bool{ToolName: true},
	}})
}

func testCtx(dir string) tools.ToolContext {
	return tools.ToolContext{
		Roots:              []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
		ActiveRootID:       "r1",
		Agent:              tools.DefaultToolProfileID,
		SessionID:          "test-session",
		RepoFileCount:      100,
		RepoFileCountKnown: true,
	}
}

func assertHasReceipt(t *testing.T, out string) {
	t.Helper()
	if _, ok := surveyreceipt.Parse(out); !ok {
		t.Fatalf("missing survey receipt in %q", out)
	}
}

func parseResponse(t *testing.T, out string) response {
	t.Helper()
	var resp response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode jq response: %v raw=%s", err, out)
	}
	return resp
}

func writeJSONFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write json", err)
	}
}

func runJq(t *testing.T, dir string, args map[string]any) (string, error) {
	t.Helper()
	tool := &Tool{Boundary: testBoundary(t)}
	return tool.Run(context.Background(), args, testCtx(dir))
}

func TestJqLiteralQuery(t *testing.T) {
	dir := t.TempDir()
	writeJSONFile(t, dir, "data.json", `{"name":"lycaon","version":3,"tags":["a","b"]}`)

	out, err := runJq(t, dir, map[string]any{"path": "data.json", "query": ".name"})
	testutil.FailErr(t, "jq", err)
	assertHasReceipt(t, out)
	resp := parseResponse(t, out)
	if len(resp.Values) != 1 || string(resp.Values[0]) != `"lycaon"` {
		t.Fatalf("values = %v", resp.Values)
	}
	if resp.Shape != nil {
		t.Fatalf("literal query should not zoom out: %+v", resp.Shape)
	}
}

func TestJqDecodesSelfIdentifyingEncodings(t *testing.T) {
	t.Parallel()
	for _, encoding := range testutil.SelfIdentifyingTextEncodings() {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			data := testutil.EncodeTextFixture(t, `{"name":"wolf"}`, encoding)
			if err := os.WriteFile(filepath.Join(dir, "data.json"), data, 0o644); err != nil {
				t.Fatalf("write JSON: %v", err)
			}
			out, err := runJq(t, dir, map[string]any{"path": "data.json", "query": ".name"})
			testutil.FailErr(t, "jq UTF-16", err)
			response := parseResponse(t, out)
			if len(response.Values) != 1 || string(response.Values[0]) != `"wolf"` {
				t.Fatalf("response = %+v", response)
			}
		})
	}
}

func TestJqWholeDocSmallIsLiteral(t *testing.T) {
	dir := t.TempDir()
	writeJSONFile(t, dir, "small.json", `{"a":1,"b":2}`)

	out, err := runJq(t, dir, map[string]any{"path": "small.json"})
	testutil.FailErr(t, "jq", err)
	resp := parseResponse(t, out)
	if resp.Shape != nil {
		t.Fatalf("small doc should be literal, got shape %+v", resp.Shape)
	}
	if len(resp.Values) != 1 {
		t.Fatalf("values = %v", resp.Values)
	}
}

func TestJqZoomOutShapeOnLargeDoc(t *testing.T) {
	dir := t.TempDir()
	// Exceed the zoom threshold.
	n := (safecmd.JQZoomBytes / 40) + 200
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf("%q", strings.Repeat("x", 48)+fmt.Sprintf("-%d", i))
	}
	writeJSONFile(t, dir, "big.json", "["+strings.Join(items, ",")+"]")

	out, err := runJq(t, dir, map[string]any{"path": "big.json"})
	testutil.FailErr(t, "jq", err)
	resp := parseResponse(t, out)
	if resp.Shape == nil {
		t.Fatalf("large doc should zoom out to shape, got %s", out)
	}
	if resp.Shape.Type != "array" || resp.Shape.Len != n {
		t.Fatalf("shape = %+v", resp.Shape)
	}
	if resp.Shape.Elem == nil || resp.Shape.Elem.Type != "string" {
		t.Fatalf("elem shape = %+v", resp.Shape.Elem)
	}
	if len(resp.Values) != 0 {
		t.Fatalf("zoom-out must not inline values: %v", resp.Values)
	}
	if resp.Diagnostics == nil || resp.Diagnostics.Hint == "" {
		t.Fatalf("expected zoom-out hint, resp=%+v", resp)
	}
}

func TestJqStreamOverflowZoomsOut(t *testing.T) {
	dir := t.TempDir()
	n := safecmd.JQMaxResults + 50
	nums := make([]string, n)
	for i := range nums {
		nums[i] = fmt.Sprintf("%d", i)
	}
	writeJSONFile(t, dir, "nums.json", "["+strings.Join(nums, ",")+"]")

	// Unpaged overflow returns a shape.
	out, err := runJq(t, dir, map[string]any{"path": "nums.json", "query": ".[]"})
	testutil.FailErr(t, "jq", err)
	resp := parseResponse(t, out)
	if resp.Shape == nil || resp.Shape.Len != n {
		t.Fatalf("expected stream zoom-out len %d, got %+v", n, resp.Shape)
	}
}

func TestJqLimitPaginationIsLiteral(t *testing.T) {
	dir := t.TempDir()
	writeJSONFile(t, dir, "five.json", `[10,11,12,13,14]`)

	out, err := runJq(t, dir, map[string]any{"path": "five.json", "query": ".[]", "limit": float64(2)})
	testutil.FailErr(t, "jq", err)
	resp := parseResponse(t, out)
	if resp.Shape != nil {
		t.Fatalf("limit forces literal, got shape %+v", resp.Shape)
	}
	if len(resp.Values) != 2 || string(resp.Values[0]) != "10" || string(resp.Values[1]) != "11" {
		t.Fatalf("values = %v", resp.Values)
	}
	if resp.ResultTotal != 5 || !resp.Truncated || resp.NextOffset == nil || *resp.NextOffset != 2 {
		t.Fatalf("paging = total=%d truncated=%v next=%v", resp.ResultTotal, resp.Truncated, resp.NextOffset)
	}

	out2, err := runJq(t, dir, map[string]any{"path": "five.json", "query": ".[]", "limit": float64(2), "offset": float64(2)})
	testutil.FailErr(t, "jq offset", err)
	resp2 := parseResponse(t, out2)
	if len(resp2.Values) != 2 || string(resp2.Values[0]) != "12" {
		t.Fatalf("offset page values = %v", resp2.Values)
	}
}

func TestJqSandboxDeniesEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LYCAON_JQ_SECRET", "leak-me")
	writeJSONFile(t, dir, "x.json", `1`)

	out, err := runJq(t, dir, map[string]any{"path": "x.json", "query": "env"})
	testutil.FailErr(t, "jq env", err)
	resp := parseResponse(t, out)
	if len(resp.Values) != 1 {
		t.Fatalf("values = %v", resp.Values)
	}
	if strings.Contains(string(resp.Values[0]), "leak-me") || string(resp.Values[0]) != "{}" {
		t.Fatalf("env must be sandboxed to empty object, got %s", resp.Values[0])
	}
}

func TestJqInvalidJSONRejects(t *testing.T) {
	dir := t.TempDir()
	writeJSONFile(t, dir, "bad.json", `{not json`)

	_, err := runJq(t, dir, map[string]any{"path": "bad.json"})
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "JQ_PARSE" {
		t.Fatalf("err = %v want JQ_PARSE", err)
	}
}

func TestJqYAMLQuery(t *testing.T) {
	dir := t.TempDir()
	payload := "phases:\n  - tools:\n    - grep\n"
	if err := os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte(payload), 0o644); err != nil {
		testutil.FailErr(t, "write yaml", err)
	}
	out, err := runJq(t, dir, map[string]any{
		"path":   "workflow.yaml",
		"format": "yaml",
		"query":  ".phases[0].tools[0]",
	})
	testutil.FailErr(t, "jq yaml", err)
	resp := parseResponse(t, out)
	if resp.Format != "yaml" {
		t.Fatalf("format = %q", resp.Format)
	}
	if len(resp.Values) != 1 || string(resp.Values[0]) != `"grep"` {
		t.Fatalf("values = %v", resp.Values)
	}
}

func TestJqTOMLQuery(t *testing.T) {
	dir := t.TempDir()
	payload := "[package]\nname = \"demo\"\n"
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(payload), 0o644); err != nil {
		testutil.FailErr(t, "write toml", err)
	}
	out, err := runJq(t, dir, map[string]any{
		"path":  "Cargo.toml",
		"query": ".package.name",
	})
	testutil.FailErr(t, "jq toml", err)
	resp := parseResponse(t, out)
	if resp.Format != "toml" {
		t.Fatalf("format = %q", resp.Format)
	}
	if len(resp.Values) != 1 || string(resp.Values[0]) != `"demo"` {
		t.Fatalf("values = %v", resp.Values)
	}
}

func TestJqPathDenied(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("{}"), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	_, err := runJq(t, dir, map[string]any{"path": ".git/config", "query": ".core"})
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "JQ_PATH_DENIED" {
		t.Fatalf("err = %v want JQ_PATH_DENIED", err)
	}
}

func TestJqInvalidQueryRejects(t *testing.T) {
	dir := t.TempDir()
	writeJSONFile(t, dir, "x.json", `1`)

	_, err := runJq(t, dir, map[string]any{"path": "x.json", "query": ".["})
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "JQ_QUERY_INVALID" {
		t.Fatalf("err = %v want JQ_QUERY_INVALID", err)
	}
}

func TestJqPathRequired(t *testing.T) {
	dir := t.TempDir()
	_, err := runJq(t, dir, map[string]any{"path": "", "query": "."})
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "JQ_PATH_REQUIRED" {
		t.Fatalf("err = %v want JQ_PATH_REQUIRED", err)
	}
}

func TestJqPathEscape(t *testing.T) {
	dir := t.TempDir()
	_, err := runJq(t, dir, map[string]any{"path": "../outside.json"})
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("err = %v want SURVEY_PATH_ESCAPE", err)
	}
}

func TestJqUpdateFilterNeverWrites(t *testing.T) {
	dir := t.TempDir()
	body := `{"name":"lycaon","count":1}`
	writeJSONFile(t, dir, "data.json", body)

	out, err := runJq(t, dir, map[string]any{"path": "data.json", "query": ".count = 2", "limit": 1})
	testutil.FailErr(t, "jq update filter", err)
	resp := parseResponse(t, out)
	if len(resp.Values) != 1 || !strings.Contains(string(resp.Values[0]), `"count":2`) {
		t.Fatalf("values = %s, want the updated document", resp.Values)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "data.json"))
	testutil.FailErr(t, "read data.json", err)
	if string(raw) != body {
		t.Fatalf("jq changed the file: %s", raw)
	}
}
