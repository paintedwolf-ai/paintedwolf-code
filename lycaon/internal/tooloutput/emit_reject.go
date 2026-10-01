package tooloutput

import (
	"strings"
)

// Structured reasons for oversized tool output.
const (
	EmitReasonUnpaginatedSurvey  = "unpaginated_survey"
	EmitReasonBareRepoScope      = "bare_repo_scope"
	EmitReasonOpaqueOverflow     = "opaque_overflow"
	EmitReasonStructuredSpillCap = "structured_spill_cap"
)

// ClassifyEmitReject classifies oversized payloads without truncation receipts.
func ClassifyEmitReject(tool string, args map[string]any, content string, maxSpillBytes int) (code string, data map[string]any) {
	cap := EffectiveMaxSpillFileBytes(maxSpillBytes)
	n := len(content)
	if n <= cap {
		return "", nil
	}
	if HasTruncationReceipt(content) {
		return "", nil
	}
	tool = strings.TrimSpace(strings.ToLower(tool))
	reason := EmitReasonOpaqueOverflow
	if IsStructuredToolJSON(content) {
		if tool == "summarize" && isBareRepoScope(args) {
			reason = EmitReasonBareRepoScope
		} else {
			reason = EmitReasonUnpaginatedSurvey
		}
	}
	data = map[string]any{
		"bytes":              n,
		"cap":                cap,
		"reason":             reason,
		"bare_repo_scope":    reason == EmitReasonBareRepoScope,
		"unpaginated_survey": reason == EmitReasonUnpaginatedSurvey,
		"opaque_overflow":    reason == EmitReasonOpaqueOverflow,
		"tool":               tool,
	}
	if reason == EmitReasonBareRepoScope {
		data["path"] = "."
	}
	if reason == EmitReasonUnpaginatedSurvey {
		if missing := unpaginatedMissingArgs(tool, args); len(missing) > 0 {
			data["missing_args"] = strings.Join(missing, ", ")
		}
	}
	return ToolResultTooLargeCode, data
}

// EnrichSpillCapReject adds spill-cap reject context for commit-time backstops.
func EnrichSpillCapReject(tool string, args map[string]any, content string, maxSpillBytes int) map[string]any {
	_, data := ClassifyEmitReject(tool, args, content, maxSpillBytes)
	if data == nil {
		cap := EffectiveMaxSpillFileBytes(maxSpillBytes)
		data = map[string]any{
			"bytes": len(content),
			"cap":   cap,
			"tool":  strings.TrimSpace(strings.ToLower(tool)),
		}
	}
	data["reason"] = EmitReasonStructuredSpillCap
	data["bare_repo_scope"] = tool == "summarize" && isBareRepoScope(args)
	if data["bare_repo_scope"] == true {
		data["path"] = "."
	}
	return data
}

func isBareRepoScope(args map[string]any) bool {
	if args == nil {
		return false
	}
	if strings.TrimSpace(stringArg(args, "content")) != "" {
		return false
	}
	if strings.TrimSpace(stringArg(args, "pattern")) != "" {
		return false
	}
	if paths, ok := args["paths"].([]any); ok && len(paths) > 0 {
		return false
	}
	path := strings.TrimSpace(stringArg(args, "path"))
	return path == "" || path == "."
}

func unpaginatedMissingArgs(tool string, args map[string]any) []string {
	switch tool {
	case "grep":
		if !hasNumericArg(args, "offset") && !hasNumericArg(args, "max_matches") {
			return []string{"offset", "max_matches"}
		}
	case "find", "list_dir":
		if !hasNumericArg(args, "offset") && !hasNumericArg(args, "max_results") && !hasNumericArg(args, "max_entries") {
			return []string{"offset", "max_results"}
		}
	case "read":
		if stringArg(args, "symbol") == "" && !hasNumericArg(args, "offset") && !hasNumericArg(args, "limit") && !hasRangesArg(args) {
			return []string{"offset", "limit", "symbol", "ranges"}
		}
	case "jq":
		if !hasNumericArg(args, "offset") && !hasNumericArg(args, "limit") {
			return []string{"offset", "limit"}
		}
	case "git_status", "git_diff":
		if !hasNumericArg(args, "offset") && !hasNumericArg(args, "limit") && !hasPathsArg(args) {
			return []string{"offset", "limit", "paths"}
		}
	}
	return nil
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, _ := args[key].(string)
	return v
}

func hasNumericArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	switch n := v.(type) {
	case float64:
		return n > 0
	case int:
		return n > 0
	case int64:
		return n > 0
	default:
		return false
	}
}

func hasPathsArg(args map[string]any) bool {
	if args == nil {
		return false
	}
	paths, ok := args["paths"].([]any)
	return ok && len(paths) > 0
}

func hasRangesArg(args map[string]any) bool {
	if args == nil {
		return false
	}
	ranges, ok := args["ranges"].([]any)
	return ok && len(ranges) > 0
}
