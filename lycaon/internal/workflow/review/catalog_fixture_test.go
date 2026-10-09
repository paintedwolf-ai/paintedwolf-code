package review_test

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"path/filepath"
	"testing"
)

func shippedToolSchemas(t *testing.T) *toolschema.Config {
	t.Helper()
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(configlayout.FindModuleRoot(), "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "LoadSchemaDir", err)
	return cfg
}
func catalogRegistry(t *testing.T) *tools.DefaultRegistry {
	t.Helper()
	reg, err := tools.NewCatalogRegistry(shippedToolSchemas(t))
	testutil.FailErr(t, "NewCatalogRegistry", err)
	return reg
}
func catalogSubmitVerdictSchema(t *testing.T) map[string]any {
	t.Helper()
	meta, ok := shippedToolSchemas(t).ToolMeta("submit_verdict")
	if !ok {
		t.Fatal("submit_verdict schema missing")
	}
	return meta.ArgsSchema
}
