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
