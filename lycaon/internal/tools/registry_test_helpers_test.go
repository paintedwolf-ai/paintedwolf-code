package tools

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func registerTestDefinition(t *testing.T, reg *DefaultRegistry, name string, handler ToolHandler) {
	t.Helper()
	testutil.FailErr(t, "register "+name, reg.RegisterDefinition(Definition{
		Meta:     ToolMeta{Name: name, Description: "test tool", ArgsSchema: map[string]any{"type": "object"}},
		Contract: toolcontract.External("test"), Handler: handler,
	}))
}
