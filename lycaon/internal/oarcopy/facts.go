// Package oarcopy maps host occurrence keys onto declared copy-fact names.
package oarcopy

import (
	"fmt"
	"strings"
)

const HostFactNamespace = "paintedwolf"

var profileNames = map[string]bool{
	"tool":                    true,
	"tool_args":               true,
	"tool_args_fingerprint":   true,
	"arg_validation_errors":   true,
	"arg_validation_reason":   true,
	"arg_validation_field":    true,
	"permission_profile":      true,
	"policy_denied":           true,
	"mcp_error_code":          true,
	"mcp_provider_id":         true,
	"mcp_tool_name":           true,
	"mcp_qualified_tool":      true,
	"mcp_provider_configured": true,
	"mcp_provider_enabled":    true,
	"mcp_call_ok":             true,
	"mcp_schema_matched":      true,
	"session_posture":         true,
	"principal":               true,
	"principal_roles":         true,
	"is_directory":            true,
	"not_found":               true,
	"path_denied":             true,
}

var publishedFactNames = map[string]string{
	"reason": "paintedwolf.rejection_reason",
	"field":  "paintedwolf.rejection_field",
}

var reasonFlags = map[string]string{
	"reference_required":  "paintedwolf.reference_required",
	"grounding_escalated": "paintedwolf.grounding_escalated",
	"bare_repo_scope":     "paintedwolf.bare_repo_scope",
	"unpaginated_survey":  "paintedwolf.unpaginated_survey",
	"opaque_overflow":     "paintedwolf.opaque_overflow",
}

// FactsFromData rewrites occurrence keys onto declared fact names and
// derives the boolean flags copy can test.
func FactsFromData(data map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range data {
		out[publishKey(key)] = value
	}
	reason, _ := out["paintedwolf.rejection_reason"].(string)
	if reason == "" {
		if raw, ok := data["reason"]; ok {
			reason = fmt.Sprint(raw)
			if reason == "<nil>" {
				reason = ""
			}
		}
	}
	if reason != "" {
		out["paintedwolf.rejection_reason"] = reason
		if flag, ok := reasonFlags[reason]; ok {
			out[flag] = true
		}
	}
	if kind, ok := data["kind"].(string); ok && kind == "redirect" {
		out["paintedwolf.redirect"] = true
	}
	return out
}

func publishKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return key
	}
	if name, ok := publishedFactNames[key]; ok {
		return name
	}
	if profileNames[key] || strings.Contains(key, ".") {
		return key
	}
	return HostFactNamespace + "." + key
}
