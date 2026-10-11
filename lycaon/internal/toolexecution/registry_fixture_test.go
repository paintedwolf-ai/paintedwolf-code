package toolexecution

import (
	"github.com/lycaon/lycaon/internal/tools"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func registerTestDefinition(t *testing.T, reg *tools.DefaultRegistry, name string, handler tools.ToolHandler) {
	t.Helper()
	testutil.FailErr(t, "register "+name, reg.RegisterDefinition(tools.Definition{
		Meta:     tools.ToolMeta{Name: name, Description: "test tool", ArgsSchema: map[string]any{"type": "object"}},
		Contract: toolcontract.External("test"), Handler: handler,
	}))
}
