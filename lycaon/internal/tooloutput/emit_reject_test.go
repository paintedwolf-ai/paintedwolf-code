package tooloutput

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClassifyEmitRejectAllowsStructuredTruncatedReceipt(t *testing.T) {
	results := make([]any, 0, 4096)
	for i := 0; i < 4096; i++ {
		results = append(results, map[string]any{"path": strings.Repeat("a", 256)})
	}
	raw, err := json.Marshal(map[string]any{
		"results":     results,
		"truncated":   true,
		"next_offset": 4096,
	})
	testutil.FailErr(t, "marshal", err)
	cap := 1024
	if len(raw) <= cap {
		t.Fatalf("test fixture must exceed spill cap: len=%d", len(raw))
	}
	if code, _ := ClassifyEmitReject("grep", nil, string(raw), cap); code != "" {
		t.Fatalf("truncated receipt should pass emit validation, got code=%q", code)
	}
}

func TestClassifyEmitRejectRejectsUnboundedCommand(t *testing.T) {
	content := strings.Repeat("y", DefaultMaxSpillFileBytes+1)
	code, data := ClassifyEmitReject("command", nil, content, 0)
	if code != ToolResultTooLargeCode {
		t.Fatalf("code=%q want %q", code, ToolResultTooLargeCode)
	}
	if data["bytes"] != len(content) {
		t.Fatalf("bytes=%v want %d", data["bytes"], len(content))
	}
}

func TestClassifyEmitRejectBareRepoScope(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"pack": map[string]any{"substance": strings.Repeat("x", 2000)},
	})
	testutil.FailErr(t, "marshal", err)
	code, data := ClassifyEmitReject("summarize", map[string]any{"path": "."}, string(raw), 1024)
	if code != ToolResultTooLargeCode {
		t.Fatalf("code=%q", code)
	}
	if data["reason"] != EmitReasonBareRepoScope {
		t.Fatalf("reason=%v", data["reason"])
	}
	if data["path"] != "." {
		t.Fatalf("path=%v", data["path"])
	}
}

func TestClassifyEmitRejectUnpaginatedGrep(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"matches": []any{map[string]any{"path": "a.go", "line": 1, "content": strings.Repeat("y", 2000)}},
	})
	testutil.FailErr(t, "marshal", err)
	code, data := ClassifyEmitReject("grep", map[string]any{"pattern": "foo"}, string(raw), 1024)
	if code != ToolResultTooLargeCode {
		t.Fatalf("code=%q", code)
	}
	if data["reason"] != EmitReasonUnpaginatedSurvey {
		t.Fatalf("reason=%v", data["reason"])
	}
	if data["missing_args"] != "offset, max_matches" {
		t.Fatalf("missing_args=%v", data["missing_args"])
	}
}

func TestClassifyEmitRejectOpaqueOverflow(t *testing.T) {
	content := strings.Repeat("z", 2048)
	code, data := ClassifyEmitReject("command", map[string]any{"command": "yes"}, content, 1024)
	if code != ToolResultTooLargeCode {
		t.Fatalf("code=%q", code)
	}
	if data["reason"] != EmitReasonOpaqueOverflow {
		t.Fatalf("reason=%v", data["reason"])
	}
}

func TestClassifyEmitRejectAllowsTruncatedReceipt(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"tail": strings.Repeat("x", 2048), "truncated": true})
	testutil.FailErr(t, "marshal", err)
	content := string(raw)
	if code, _ := ClassifyEmitReject("command", nil, content, 1024); code != "" {
		t.Fatalf("expected pass with truncation receipt, got %q", code)
	}
}

func TestEnrichSpillCapRejectStructuredReason(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"pack": map[string]any{"x": strings.Repeat("a", 2000)}})
	testutil.FailErr(t, "marshal", err)
	data := EnrichSpillCapReject("summarize", map[string]any{"path": "."}, string(raw), 1024)
	if data["reason"] != EmitReasonStructuredSpillCap {
		t.Fatalf("reason=%v", data["reason"])
	}
}

func TestClassifyEmitRejectUnpaginatedGitStatus(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"available": true,
		"files":     []any{map[string]any{"path": strings.Repeat("p", 2000), "status": " M"}},
	})
	testutil.FailErr(t, "marshal", err)
	code, data := ClassifyEmitReject("git_status", map[string]any{}, string(raw), 1024)
	if code != ToolResultTooLargeCode {
		t.Fatalf("code=%q", code)
	}
	if data["reason"] != EmitReasonUnpaginatedSurvey {
		t.Fatalf("reason=%v", data["reason"])
	}
	if data["missing_args"] != "offset, limit, paths" {
		t.Fatalf("missing_args=%v", data["missing_args"])
	}
	_, data = ClassifyEmitReject("git_status", map[string]any{"paths": []any{"src"}}, string(raw), 1024)
	if _, present := data["missing_args"]; present {
		t.Fatalf("a narrowed call must not be told to narrow: %v", data)
	}
	_, data = ClassifyEmitReject("git_diff", map[string]any{"stat": true}, string(raw), 1024)
	if data["missing_args"] != "offset, limit, paths" {
		t.Fatalf("git_diff missing_args=%v", data["missing_args"])
	}
}

func TestClassifyEmitRejectDoesNotReadReceiptFromOpaqueText(t *testing.T) {
	content := strings.Repeat("a", 2048) + `{"truncated":true}`
	if code, _ := ClassifyEmitReject("command", nil, content, 1024); code != ToolResultTooLargeCode {
		t.Fatalf("opaque output reject=%q want %q", code, ToolResultTooLargeCode)
	}
}
