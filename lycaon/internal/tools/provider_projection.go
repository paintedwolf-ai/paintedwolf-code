package tools

import (
	"strings"

	"github.com/lycaon/lycaon/internal/toolschema"
)

// ProjectProviderToolMeta derives generation guidance without changing execution schemas.
func ProjectProviderToolMeta(meta ToolMeta) ToolMeta {
	out := meta
	out.Description = strings.TrimSpace(meta.Description)
	out.ArgsSchema = toolschema.ProjectArgsSchema(meta.ArgsSchema)
	return out
}

// PruneCommandReadOnlySchema removes the mutating, external-execution and
// capture parameters from the command schema when a profile or surface limits
// command to read-only native utilities.
func PruneCommandReadOnlySchema(schema map[string]any) map[string]any {
	out := toolschema.ProjectArgsSchema(schema)
	props, _ := out["properties"].(map[string]any)
	for _, key := range []string{
		"capability_request",
		"secret_use",
		"snapshot_capture",
		"socks_proxy",
		"terminal_capture",
		"verification",
	} {
		delete(props, key)
	}
	return out
}

// PruneCommandProgressSchema removes the optional progress_update parameter
// from the command schema when update_progress is not offered.
func PruneCommandProgressSchema(schema map[string]any) map[string]any {
	out := toolschema.ProjectArgsSchema(schema)
	props, _ := out["properties"].(map[string]any)
	delete(props, "progress_update")
	return out
}
