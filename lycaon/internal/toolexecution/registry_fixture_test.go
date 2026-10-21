package toolexecution

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func registerTestDefinition(t *testing.T, reg *tools.DefaultRegistry, name string, handler tools.ToolHandler) {
	t.Helper()
	testutil.FailErr(t, "register "+name, reg.RegisterDefinition(tools.Definition{
		Meta:     tools.ToolMeta{Name: name, Description: "test tool", ArgsSchema: map[string]any{"type": "object"}},
		Contract: toolcontract.External("test"), Handler: handler,
	}))
}

// catalogRegistry publishes native tools with shipped metadata, as host boot does.
func catalogRegistry(t *testing.T) *tools.DefaultRegistry {
	t.Helper()
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "LoadSchemaDir", err)
	reg, err := tools.NewCatalogRegistry(cfg)
	testutil.FailErr(t, "NewCatalogRegistry", err)
	return reg
}
