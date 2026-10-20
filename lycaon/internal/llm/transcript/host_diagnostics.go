package transcript

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
)

const (
	maxHostDiagnosticBytes    = 2 << 10
	maxVerdictDiagnosticBytes = 12 << 10
	maxDiagnosticValueBytes   = 1024
	maxDiagnosticItems        = 8
	maxDiagnosticRepairDepth  = 2
	maxDiagnosticValueDepth   = 10
)

// Order preserves effect certainty, identities and parser locations before
// larger inventories.
// Executable replacements and examples are deliberately absent: partial calls
// cannot be presented as equivalent repairs.
var diagnosticFields = []string{
	"program_completed_effects", "program_uncertain_effects", "program_replay_available", "program_effect_details_omitted",
	"program_executed_children", "program_executed_reads", "native_operation", "native_command",
	"source_start_byte", "source_end_byte", "source_line", "source_column",
	"program_id", "execution_id", "node_id", "source_unit_id", "model_tool_call_id", "program_detail_ref",
	"program_replay_limitation", "program_static_not_started", "program_static_untaken",
	"program_progress_state", "program_progress_ref", "program_progress_ref_omitted",
	"program_progress_owner_invoked", "program_progress_execution_id", "program_progress_detail_ref",
	"program_completed_details_retained_omitted", "program_unknown_details_retained_omitted", "program_stopped_details_omitted",
	"program_stopped_details", "program_completed_details", "program_unknown_details",
	"action", "work_id", "question_id", "revision", "workflow_phase", "agent",
	"field", "field_path", "path", "expected_type", "json_malformed", "json_encoded",
	"json_unexpected_token_at", "json_unterminated_string_at", "json_trailing_text_at",
	"found_under", "belongs_under", "close_before", "did_you_mean", "reason", "text_base_changed",
	"index", "type", "locators", "locators_truncated", "interactive_truncated", "timeout",
	"work_ids", "job_ids", "missing_claim_ids", "expected_claim_ids", "json_open_paths",
	"json_trailing_members", "json_trailing_belongs_under", "misplaced_fields", "conflict_keys",
	"issues", "schema_issues", "repairs", "unknown_groups", "unaccounted_group_ids",
	"completed_actions", "interactive_controls",
}

// The canonical result retains every detail. Only the outbound data projection
// selects whole observations, with byte, item, and nested-repair bounds.
func boundedHostDiagnostics(details map[string]any, limit int) map[string]any {
	raw, err := json.Marshal(details)
	if err != nil {
		return nil
	}
	var source map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&source) != nil {
		return nil
	}
	out := projectDiagnosticFields(source, limit, 0)
	for _, name := range []string{"program_completed_details", "program_unknown_details", "program_stopped_details"} {
		rows, _ := source[name].([]any)
		kept, _ := out[name].([]any)
		if _, present := out["program_effect_details_omitted"]; present && len(kept) < len(rows) {
			out["program_effect_details_omitted"] = true
		}
	}
	return out
}

func projectDiagnosticFields(source map[string]any, limit, depth int) map[string]any {
	out := map[string]any{}
	for _, key := range diagnosticFields {
		value, ok := source[key]
		if !ok {
			continue
		}
		if rows, ok := value.([]any); ok {
			out = projectDiagnosticList(out, key, rows, limit, depth)
			continue
		}
		if !diagnosticValueFits(value) {
			continue
		}
		candidate := maps.Clone(out)
		candidate[key] = value
		if diagnosticFits(candidate, limit) {
			out = candidate
		}
	}
	return out
}

func projectDiagnosticList(out map[string]any, key string, rows []any, limit, depth int) map[string]any {
	kept := []any{}
	candidate := maps.Clone(out)
	candidate[key], candidate[key+"_omitted"] = kept, len(rows)
	if !diagnosticFits(candidate, limit) {
		return out
	}
	out = candidate
	if key == "repairs" && depth >= maxDiagnosticRepairDepth {
		return out
	}
	for _, row := range rows[:min(len(rows), maxDiagnosticItems)] {
		if repair, ok := row.(map[string]any); ok && key == "repairs" {
			details, _ := repair["details"].(map[string]any)
			row = map[string]any{"code": repair["code"], "details": projectDiagnosticFields(details, maxDiagnosticValueBytes, depth+1), "details_omitted": true}
		}
		if !diagnosticValueFits(row) {
			break
		}
		kept = append(kept, row)
		candidate = maps.Clone(out)
		candidate[key], candidate[key+"_omitted"] = kept, len(rows)-len(kept)
		if !diagnosticFits(candidate, limit) {
			break
		}
		out = candidate
	}
	return out
}

func diagnosticFits(value map[string]any, limit int) bool {
	encoded, err := json.Marshal(value)
	return err == nil && len(encoded) <= limit
}

func diagnosticValueFits(value any) bool {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxDiagnosticValueBytes {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	depth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			return err == io.EOF
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			if depth > maxDiagnosticValueDepth {
				return false
			}
		}
	}
}
