package promptloop

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestToolSchemaOrderIsStableAcrossCatalogEnumeration(t *testing.T) {
	a := tools.ToolMeta{Name: "read", ArgsSchema: map[string]any{"type": "object"}}
	b := tools.ToolMeta{Name: "command", ArgsSchema: map[string]any{"type": "object"}}
	plan := toolsurface.Compile([]string{"read", "command"}, nil)
	for _, project := range []func([]tools.ToolMeta) []tools.ToolMeta{
		trimPromptToolMetas,
		func(metas []tools.ToolMeta) []tools.ToolMeta {
			return trimCoordinatorToolMetasForPlan(plan, metas, nil)
		},
	} {
		first, err := json.Marshal(project([]tools.ToolMeta{a, b}))
		if err != nil {
			t.Fatalf("marshal tool schemas: %v", err)
		}
		second, err := json.Marshal(project([]tools.ToolMeta{b, a}))
		if err != nil {
			t.Fatalf("marshal tool schemas: %v", err)
		}
		if string(first) != string(second) {
			t.Fatalf("schema order depends on catalog enumeration: %s != %s", first, second)
		}
	}
}
