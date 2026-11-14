package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/tools"
)

// ApplyTaskSpawnAllowlistSchema narrows task(agent_type=…) to the turn spawn roster.
// A nil roster leaves the schema unchanged; an empty roster allows no agents.
func ApplyTaskSpawnAllowlistSchema(meta tools.ToolMeta, allowedAgents []string) tools.ToolMeta {
	if strings.TrimSpace(meta.Name) != "task" || allowedAgents == nil {
		return meta
	}
	out := meta
	schema := jsonvalue.CloneMap(meta.ArgsSchema)
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		schema["properties"] = props
	}
	agentProp, _ := props["agent_type"].(map[string]any)
	if agentProp == nil {
		agentProp = map[string]any{"type": "string"}
	}
	enum := make([]any, len(allowedAgents))
	for i, id := range allowedAgents {
		enum[i] = id
	}
	agentProp["enum"] = enum
	props["agent_type"] = agentProp
	out.ArgsSchema = schema
	return out
}
