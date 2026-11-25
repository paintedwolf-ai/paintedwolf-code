package guidance

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRenderCompactionSystemPrompts(t *testing.T) {
	setTestRenderer(t)
	for _, ref := range []string{
		CompactionSessionSummarySystemRef,
		CompactionChunkSummarySystemRef,
	} {
		out, err := RenderCompactionSystemPrompt(context.Background(), ref)
		testutil.FailErr(t, "RenderCompactionSystemPrompt "+ref, err)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("empty render for %q", ref)
		}
	}
}

func TestRenderCuratorPrompts(t *testing.T) {
	setTestRenderer(t)
	sys, err := RenderCuratorSystemPrompt(context.Background())
	testutil.FailErr(t, "RenderCuratorSystemPrompt", err)
	if strings.TrimSpace(sys) == "" {
		t.Fatal("empty curator system prompt")
	}
	user, err := RenderCuratorUserPrompt(context.Background(), map[string]any{
		"focus":  "entry",
		"budget": 3,
		"candidates": []map[string]any{
			{"handle": "read#1", "path": "a.go", "lines": "1", "preview": "1| package a"},
		},
	})
	testutil.FailErr(t, "RenderCuratorUserPrompt", err)
	if !strings.Contains(user, "entry") {
		t.Fatalf("user prompt missing focus: %q", user)
	}
}

func TestRenderUtilityPromptsAndSurveySeparation(t *testing.T) {
	setTestRenderer(t)
	fixture := map[string]any{
		"query": "q", "task_hint": "", "max_seeds": 3, "max_hosts": 4,
		"min_leads": 2, "max_leads": 5, "expand_min": 2, "expand_max": 4,
		"hit_target": 5, "period_current": true, "period": "current",
		"weekday": "Saturday", "date": "2026-08-15", "cutoff": "2024-01",
		"goal": "ship", "tool": "command", "command": "ls", "steps": "1. ls",
		"user_intent": "ship the feature", "action": "command ls",
		"result_handle": "h1", "assignment": "fix", "outcome": "complete",
		"current_request": "continue", "prior_continuation": "{}",
		"recent_messages": []map[string]any{{"role": "user", "origin": "user", "authority": "user", "trust_tier": "trusted", "content": "hi"}},
		"focus":           "entry", "budget": 3,
		"candidates": []map[string]any{{"handle": "read#1", "path": "a.go", "lines": "1", "preview": "1| package a"}},
	}
	for _, ref := range prompts.ZeroToolUtilityTemplates() {
		out, err := prompts.RenderUtilityPrompt(context.Background(), ref, fixture)
		testutil.FailErr(t, "RenderUtilityPrompt "+ref, err)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("empty render for %q", ref)
		}
		if _, err := RenderGuidance(context.Background(), ref, fixture); err == nil {
			t.Fatalf("RenderGuidance accepted zero-tool utility template %q without RenderUtilityPrompt", ref)
		}
	}

	for _, ref := range []string{
		SurveyJQZoomRef,
		SurveyGrepEmptyTextRef,
		SurveyGrepEmptyStructuralRef,
		SurveyGrepScopeDenseRef,
	} {
		out, err := RenderGuidance(context.Background(), ref, map[string]any{
			"file_count": 12, "count": 8, "bytes": 4000,
		})
		testutil.FailErr(t, "RenderGuidance "+ref, err)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("empty render for %q", ref)
		}
		if _, err := prompts.RenderUtilityPrompt(context.Background(), ref, nil); err == nil {
			t.Fatalf("RenderUtilityPrompt accepted non-allowlisted ref %q", ref)
		}
	}

	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "implement",
		Profiles:  profiles,
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
	testutil.FailErr(t, "build reader", err)
	gr := prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	for _, ref := range []string{
		"survey/grep-specifics",
		"survey/find-specifics",
		"survey/read-specifics",
		"survey/jq-specifics",
	} {
		if _, err := RenderGuidance(context.Background(), ref, nil); err == nil {
			t.Fatalf("RenderGuidance accepted non-structural ref %q without Reader", ref)
		}
		out, err := gr.RenderWithReader(context.Background(), reader, ref, nil)
		testutil.FailErr(t, "RenderWithReader "+ref, err)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("empty render for %q", ref)
		}
	}
}

func TestRenderCommandOutputOmittedPairedWitnesses(t *testing.T) {
	setTestRenderer(t)
	sedAdmitted := promptstest.StockToolReach(t, toolreach.ProgramTool).WithPrograms(func(name string) bool {
		return name == "sed"
	})
	sedDenied := promptstest.StockToolReach(t, toolreach.ProgramTool).WithPrograms(func(name string) bool {
		return name != "sed"
	})
	missingSchema := toolreach.Offered([]string{toolreach.ProgramTool}).WithPrograms(func(name string) bool {
		return name == "sed"
	})
	narrowedNoCommand := toolreach.Offered([]string{toolreach.ProgramTool}).WithPrograms(func(name string) bool {
		return name == "sed"
	}).WithSchemas(map[string]map[string]any{
		toolreach.ProgramTool: {"type": "object", "properties": map[string]any{}},
	})
	withoutCommand := promptstest.StockToolReach(t, "recall")

	pos, err := RenderCommandOutputOmitted(t.Context(), sedAdmitted, "4,182", "184 KB", "@output/prog.stdout", 251, 400, 5000, 200000, true)
	testutil.FailErr(t, "render with sed admitted", err)
	if !strings.Contains(pos, "(sed -n '251,400p' @output/prog.stdout)") {
		t.Fatalf("positive witness missing sed command hint: %q", pos)
	}

	quotedPath := "@output/my file's.stdout"
	quotedOut, err := RenderCommandOutputOmitted(t.Context(), sedAdmitted, "4,182", "184 KB", quotedPath, 251, 400, 5000, 200000, true)
	testutil.FailErr(t, "render quoted path", err)
	if !strings.Contains(quotedOut, quotedPath) {
		t.Fatalf("quoted witness lost raw saved path: %q", quotedOut)
	}
	if !strings.Contains(quotedOut, `(sed -n '251,400p' @output/'my file'\''s.stdout')`) {
		t.Fatalf("quoted witness did not safely quote command operand: %q", quotedOut)
	}

	for label, req := range map[string]toolreach.Request{
		"sed denied":          sedDenied,
		"missing schema":      missingSchema,
		"narrowed no command": narrowedNoCommand,
		"without command":     withoutCommand,
	} {
		neg, err := RenderCommandOutputOmitted(t.Context(), req, "4,182", "184 KB", quotedPath, 251, 400, 5000, 200000, true)
		testutil.FailErr(t, "render "+label, err)
		if strings.Contains(neg, "sed -n") {
			t.Fatalf("%s witness leaked sed command hint: %q", label, neg)
		}
		if !strings.Contains(neg, quotedPath) {
			t.Fatalf("%s witness lost the saved output path: %q", label, neg)
		}
	}
}

func cloneNestedSchemaMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if m, ok := v.(map[string]any); ok {
			out[k] = cloneNestedSchemaMap(m)
			continue
		}
		out[k] = v
	}
	return out
}

func TestRenderContinuationProjectedRecallArgumentWitnesses(t *testing.T) {
	setTestRenderer(t)
	fullRecall := promptstest.ProjectedToolSchemas(t, "recall")["recall"]
	historyOnly := cloneNestedSchemaMap(fullRecall)
	delete(historyOnly["properties"].(map[string]any), "query")

	historyIndexOnly := cloneNestedSchemaMap(historyOnly)
	histProps := historyIndexOnly["properties"].(map[string]any)["history"].(map[string]any)["properties"].(map[string]any)
	delete(histProps, "message_id")
	delete(histProps, "format")

	queryOnly := cloneNestedSchemaMap(fullRecall)
	delete(queryOnly["properties"].(map[string]any), "history")

	emptyNestedHistoryWithQuery := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"history": map[string]any{"type": "object"},
			"query":   map[string]any{"type": "string"},
		},
	}

	occurrence := map[string]any{
		"level":               "summary",
		"summary_through_ord": int64(10),
		"through_ord":         int64(12),
		"facts":               []string{"build passed"},
		"compacted_sources": []map[string]any{
			{"handles": []string{"read#3"}, "tool": "read"},
			{"message_id": "msg-7", "tool": "command"},
		},
	}

	for _, tc := range []struct {
		name           string
		schemas        map[string]map[string]any
		wantHistoryIdx bool
		wantHistoryRec bool
		wantQuery      bool
	}{
		{"full schema", map[string]map[string]any{"recall": fullRecall}, true, true, true},
		{"history only", map[string]map[string]any{"recall": historyOnly}, true, true, false},
		{"history index only", map[string]map[string]any{"recall": historyIndexOnly}, true, false, false},
		{"query only", map[string]map[string]any{"recall": queryOnly}, false, false, true},
		{"empty nested history object with query", map[string]map[string]any{"recall": emptyNestedHistoryWithQuery}, false, false, true},
		{"missing schema", nil, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := toolreach.Offered([]string{"recall"}).WithSchemas(tc.schemas)
			out, err := RenderGuidanceWithReach(t.Context(), req, CompactionContinuationRecordRef, occurrence)
			testutil.FailErr(t, "RenderGuidanceWithReach continuation-record", err)

			if strings.Contains(out, `recall(history={"after_ord":0,"through_ord":12})`) != tc.wantHistoryIdx {
				t.Fatalf("history index presence = %v, want %v:\n%s", !tc.wantHistoryIdx, tc.wantHistoryIdx, out)
			}
			if strings.Contains(out, `"format":"record"`) != tc.wantHistoryRec {
				t.Fatalf("history record presence = %v, want %v:\n%s", !tc.wantHistoryRec, tc.wantHistoryRec, out)
			}
			if strings.Contains(out, `recall(query="handle:read#3")`) != tc.wantQuery {
				t.Fatalf("query presence = %v, want %v:\n%s", !tc.wantQuery, tc.wantQuery, out)
			}

			bodyMissing, err := RenderGuidanceWithReach(t.Context(), req, "recall/body-missing", nil)
			testutil.FailErr(t, "RenderGuidanceWithReach body-missing", err)
			if strings.Contains(bodyMissing, `query="handle:<returned handle>"`) != tc.wantQuery {
				t.Fatalf("body-missing query presence = %v, want %v: %q", !tc.wantQuery, tc.wantQuery, bodyMissing)
			}
		})
	}
}

func TestRenderOverlayRebaseConflictProjectedArgumentWitnesses(t *testing.T) {
	setTestRenderer(t)
	offered := []string{"preview_overlay", "promote_overlay", "instruct_worker"}
	full := promptstest.ProjectedToolSchemas(t, offered...)
	outcomes := []api.OverlayRebaseOutcome{{
		OverlayID: "ov-child",
		Status:    api.WorkerMergeStatusRebasing,
		Conflicts: []string{"pkg/main.go"},
	}}

	narrowPromote := maps.Clone(full)
	narrowPromoteSchema := cloneNestedSchemaMap(full["promote_overlay"])
	resItemsProps := narrowPromoteSchema["properties"].(map[string]any)["resolutions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	delete(resItemsProps, "content")
	narrowPromote["promote_overlay"] = narrowPromoteSchema

	emptyNestedPromote := maps.Clone(full)
	emptyNestedPromoteSchema := cloneNestedSchemaMap(full["promote_overlay"])
	emptyNestedPromoteSchema["properties"].(map[string]any)["resolutions"].(map[string]any)["items"] = map[string]any{"type": "object"}
	emptyNestedPromote["promote_overlay"] = emptyNestedPromoteSchema

	missingInstruct := maps.Clone(full)
	delete(missingInstruct, "instruct_worker")

	for _, tc := range []struct {
		name         string
		schemas      map[string]map[string]any
		wantPreview  bool
		wantPromote  bool
		wantInstruct bool
	}{
		{"all schemas admitted", full, true, true, true},
		{"promote_overlay narrowed omits content", narrowPromote, true, false, true},
		{"promote_overlay empty nested items object", emptyNestedPromote, true, false, true},
		{"instruct_worker missing schema", missingInstruct, true, true, false},
		{"all schemas missing", nil, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := toolreach.Offered(offered).WithSchemas(tc.schemas)
			out, err := RenderOverlayRebaseConflictBanners(t.Context(), req, "ov-parent", outcomes)
			testutil.FailErr(t, "RenderOverlayRebaseConflictBanners", err)
			if !strings.Contains(out, "Code: OVERLAY_REBASE_CONFLICT") || !strings.Contains(out, "`pkg/main.go`") {
				t.Fatalf("conflict header/code missing:\n%s", out)
			}
			if strings.Contains(out, "`preview_overlay`") != tc.wantPreview {
				t.Fatalf("preview_overlay = %v, want %v:\n%s", !tc.wantPreview, tc.wantPreview, out)
			}
			if strings.Contains(out, "`promote_overlay`") != tc.wantPromote {
				t.Fatalf("promote_overlay = %v, want %v:\n%s", !tc.wantPromote, tc.wantPromote, out)
			}
			if strings.Contains(out, "`instruct_worker`") != tc.wantInstruct {
				t.Fatalf("instruct_worker = %v, want %v:\n%s", !tc.wantInstruct, tc.wantInstruct, out)
			}
		})
	}
}
