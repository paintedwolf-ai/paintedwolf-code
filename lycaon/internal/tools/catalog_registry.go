package tools

import (
	"fmt"
	"strings"
)

// RegisteredToolSet returns a set of registered tool names from metadata.
func RegisteredToolSet(reg *DefaultRegistry) map[string]bool {
	out := make(map[string]bool)
	if reg == nil {
		return out
	}
	for _, meta := range reg.List() {
		out[meta.Name] = true
	}
	return out
}

// These claims must have handlers before the engine serves tools.
var bootToolClaimNames = []string{
	"command", "task", "scan_pack", "scan_list", "scan_summary", "scan_query", "scan_compare",
	"workflow_compose", "workflow_compose_from_template", "workflow_persist",
	"workflow_catalog_summaries", "workflow_user_feedback", "ask_user", "workflow_advance", "fanout_plan", "submit_verdict",
	"wait",
	"pack_board", "read_findings", "read_review", "plan_from_blueprint",
	"web_search", "fetch_url",
}

// ValidateBootToolClaimsHonest fails when a boot claim is not registered on the registry.
func ValidateBootToolClaimsHonest(reg *DefaultRegistry) error {
	registered := RegisteredToolSet(reg)
	var missing []string
	for _, name := range bootToolClaimNames {
		if !registered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("boot tool claims not registered: %s", strings.Join(missing, ", "))
	}
	return nil
}
