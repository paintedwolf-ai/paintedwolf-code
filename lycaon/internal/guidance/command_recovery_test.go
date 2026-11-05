package guidance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandRecoveryStateDoesNotComeFromDiagnosticProse(t *testing.T) {
	SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	for _, text := range []string{"No command operation ran or changed files.", "Nothing executed. No files changed.", "completed_effects=0", "COMMAND_SYNTAX_INVALID", "exit status 0"} {
		if view := commandRecoveryView(map[string]any{"reason": text, "stderr": text, "command": text}); view != nil {
			t.Fatalf("prose selected execution state: %#v", view)
		}
	}
	hints, err := LoadHintConfigStock()
	testutil.FailErr(t, "load hints", err)
	entry := hints.HintCodes["COMMAND_SYNTAX_INVALID"]
	for _, state := range []string{"not_started", "observed", "active", ""} {
		details := map[string]any{"tool": "command", "source_line": 1, "source_column": 3, "expected_form": "complete Bash source", "parser_kind": "unclosed_quote", "parser_message": "reached EOF without closing quote", "command_execution": state, "program_executed_children": 3, "program_completed_effects": 1, "program_uncertain_effects": 1}
		if state == "not_started" {
			details["program_executed_children"], details["program_completed_effects"], details["program_uncertain_effects"] = 0, 0, 0
		}
		got, err := RenderUnifiedRejectBlock(t.Context(), "COMMAND_SYNTAX_INVALID", entry, details)
		testutil.FailErr(t, "render failure", err)
		if strings.Contains(got, "No command operation ran") != (state == "not_started") {
			t.Fatalf("state %s contradicted effects: %s", state, got)
		}
		if state == "observed" && (!strings.Contains(got, "Completed effects: 1") || !strings.Contains(got, "Uncertain effects: 1")) {
			t.Fatalf("lost certainty: %s", got)
		}
	}
}

func TestCommandRecoveryRejectsContradictoryOrUnknownCounts(t *testing.T) {
	for _, count := range []any{-1, 1.5, "0", nil} {
		data := map[string]any{"tool": "command", "command_execution": "observed", "program_completed_effects": count, "program_uncertain_effects": 0, "program_executed_children": 1}
		if view := commandRecoveryView(data); view != nil {
			t.Fatalf("invalid count selected state: %#v", view)
		}
	}
	if view := commandRecoveryView(map[string]any{"tool": "command", "command_execution": "not_started", "program_executed_children": 1}); view != nil {
		t.Fatalf("executed operation erased: %#v", view)
	}
	if view := commandRecoveryView(map[string]any{"tool": "command", "command_execution": "not_started"}); view != nil {
		t.Fatalf("missing observation became nonexecution: %#v", view)
	}
}
