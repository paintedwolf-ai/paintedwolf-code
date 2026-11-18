package guard_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestApplySpawnAllowlistSchema_setsEnum(t *testing.T) {
	meta := tools.ToolMeta{
		Name: "task",
		ArgsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent_type": map[string]any{"type": "string"},
			},
		},
	}
	allowed := inject.ResolveAgentRoster(spawn.SurfaceImplementRouting, spawn.AmbientAllowedAgents(), 1, false, true).Effective
	out := guard.ApplySpawnAllowlistSchema(meta, allowed)
	props := out.ArgsSchema["properties"].(map[string]any)
	agentProp := props["agent_type"].(map[string]any)
	enum, ok := agentProp["enum"].([]any)
	if !ok || len(enum) != len(allowed) {
		t.Fatalf("agent_type enum = %v want %d entries", agentProp["enum"], len(allowed))
	}
	for i, id := range allowed {
		if enum[i] != id {
			t.Fatalf("enum[%d] = %v want %q", i, enum[i], id)
		}
	}
}

// Nil means no roster applies; an empty non-nil roster narrows the enum to nothing —
// the caller withholds the tool, this is the schema-level backstop.
func TestApplySpawnAllowlistSchema_nilVsEmpty(t *testing.T) {
	meta := tools.ToolMeta{
		Name: "task",
		ArgsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent_type": map[string]any{"type": "string"},
			},
		},
	}
	unchanged := guard.ApplySpawnAllowlistSchema(meta, nil)
	props := unchanged.ArgsSchema["properties"].(map[string]any)
	if _, ok := props["agent_type"].(map[string]any)["enum"]; ok {
		t.Fatal("nil roster must leave the schema unnarrowed")
	}

	narrowed := guard.ApplySpawnAllowlistSchema(meta, []string{})
	props = narrowed.ArgsSchema["properties"].(map[string]any)
	enum, ok := props["agent_type"].(map[string]any)["enum"].([]any)
	if !ok || len(enum) != 0 {
		t.Fatalf("empty roster enum = %v, want empty", props["agent_type"])
	}
}

func TestApplySpawnAllowlistSchema_narrowsAgentChanges(t *testing.T) {
	meta := tools.ToolMeta{
		Name: "instruct_worker",
		ArgsSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"agent_type": map[string]any{"type": "string"}},
		},
	}
	out := guard.ApplySpawnAllowlistSchema(meta, []string{"repo-researcher"})
	enum, _ := out.ArgsSchema["properties"].(map[string]any)["agent_type"].(map[string]any)["enum"].([]any)
	if len(enum) != 1 || enum[0] != "repo-researcher" {
		t.Fatalf("instruct_worker agent_type enum = %v", enum)
	}
}
