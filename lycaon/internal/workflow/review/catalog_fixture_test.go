package review_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
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
