package toolfeedback

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestArgumentRecoveryPreservesSourcePrerequisite(t *testing.T) {
	bp := stockBlockPlane(t)
	ctx := tools.WithRecoveryTools(t.Context(), []string{"read", "edit", "write"})
	for _, rejected := range []*toolrejection.ToolReject{
		{Code: "EDIT_OLD_STRING_NOT_FOUND", Data: map[string]any{"path": "app.py", "old_len": 40, "basis_lines": 12}},
		{Code: "TOOL_ARGS_INVALID", Data: map[string]any{
			"suggested_tool": "write", "reason": "missing old_string",
			"missing_required_arguments": `{"missing_required":["old_string"],"expected_types":{"old_string":"string"},"received_keys":["new_string","path"]}`,
		}},
	} {
		err := bp.RejectObservation(ctx, "edit", "implement", nil, rejected)
		refusal, ok := guidance.RefusalFromError(err)
		if !ok || refusal.Code() != rejected.Code {
			t.Fatalf("expected %s, got %v", rejected.Code, err)
		}
		if rejected.Code == "TOOL_ARGS_INVALID" {
			copy := refusal.Copy["fix"] + refusal.Copy["instead"]
			if !strings.Contains(copy, "old_string") || !strings.Contains(copy, "current source") || !strings.Contains(copy, "source-read prerequisite") {
				t.Fatalf("schema recovery lost source repair: %s", copy)
			}
			if strings.Contains(copy, "`write`") || strings.Contains(copy, "whole file") {
				t.Fatalf("schema recovery changed operations: %s", copy)
			}
		}
	}
}

func TestArtifactRecoveryUsesCurrentOfferAndInventory(t *testing.T) {
	bp := stockBlockPlane(t)
	for _, tc := range []struct {
		name    string
		offered []string
		want    string
	}{
		{"capture", []string{"surface_note", "capture_page"}, "`capture_page` is offered"},
		{"live page", []string{"surface_note", "page_snapshot"}, "observed page_id"},
		{"discovery", []string{"surface_note", "request_tools"}, "discovery does not capture"},
		{"neither", []string{"surface_note"}, "report the capture limitation"},
		{"unknown", nil, "report the capture limitation"},
	} {
		for _, ids := range [][]string{nil, {}, {"still-1", "still-2"}} {
			t.Run(tc.name+"/"+strings.Join(ids, ","), func(t *testing.T) {
				ctx := tools.WithRecoveryTools(t.Context(), tc.offered)
				err := bp.RejectObservation(ctx, "surface_note", "implement", nil, &toolrejection.ToolReject{
					Code: "SURFACE_NOTE_ARTIFACT_UNKNOWN", Data: map[string]any{
						"surface_presentable_ids": ids, "can_capture_page": true, "can_request_tools": true,
					},
				})
				refusal, ok := guidance.RefusalFromError(err)
				if !ok || refusal.Code() != "SURFACE_NOTE_ARTIFACT_UNKNOWN" {
					t.Fatalf("missing artifact refusal: %v", err)
				}
				fix := refusal.Copy["fix"]
				if !strings.Contains(fix, tc.want) || len(fix) > 1000 {
					t.Fatalf("incorrect or excessive recovery: %s", fix)
				}
				if (len(ids) == 0) != strings.Contains(fix, "No presentable still") {
					t.Fatalf("inventory state mismatch: %s", fix)
				}
				for _, tool := range []string{"capture_page", "page_snapshot", "request_tools"} {
					if strings.Contains(fix, "`"+tool+"`") && !slices.Contains(tc.offered, tool) {
						t.Fatalf("unoffered recovery tool %s: %s", tool, fix)
					}
				}
				if len(ids) > 0 && !strings.Contains(fix, "still-1, still-2") {
					t.Fatalf("lost existing visual choice: %s", fix)
				}
			})
		}
	}
}
