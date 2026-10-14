package tooloutput

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
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

func TestEmitRecoveryFieldsBelongToReceivingSchema(t *testing.T) {
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(testutil.CheckoutRoot(t), "lycaon/config/packs/painted-wolf/platform/tools/schemas"))
	testutil.FailErr(t, "load receiving tool schemas", err)
	raw, err := json.Marshal(map[string]any{"matches": []any{map[string]any{"content": strings.Repeat("x", 2048)}}})
	testutil.FailErr(t, "marshal oversized survey", err)
	checked := 0
	for tool := range schemas.Tools {
		code, data := ClassifyEmitReject(tool, nil, string(raw), 1024)
		missing, _ := data["missing_args"].(string)
		if missing == "" {
			continue
		}
		checked++
		t.Run(tool, func(t *testing.T) {
			if code != ToolResultTooLargeCode {
				t.Fatalf("recovery fields accompanied unexpected rejection %q", code)
			}
			meta, ok := schemas.ToolMeta(tool)
			if !ok {
				t.Fatalf("missing receiving schema for %s", tool)
			}
			accepted := toolschema.ArgFieldPaths(meta.ArgsSchema)
			for _, field := range strings.Split(missing, ", ") {
				if !slices.Contains(accepted, field) {
					t.Errorf("recovery recommends %s, absent from %s schema", field, tool)
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("no schema-bound recovery fields were checked")
	}
}

func TestListingRecoveryUsesItsOwnPaginationFields(t *testing.T) {
	for _, tc := range []struct {
		tool, limit, foreign string
	}{
		{"list_dir", "max_entries", "max_results"},
		{"find", "max_results", "max_entries"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			for _, args := range []map[string]any{nil, {tc.foreign: 1}} {
				if got := unpaginatedMissingArgs(tc.tool, args); !slices.Equal(got, []string{"offset", tc.limit}) {
					t.Fatalf("pagination recovery = %v", got)
				}
			}
			for _, field := range []string{"offset", tc.limit} {
				if got := unpaginatedMissingArgs(tc.tool, map[string]any{field: 1}); len(got) != 0 {
					t.Fatalf("already bounded by %s but recommends %v", field, got)
				}
			}
		})
	}
}
