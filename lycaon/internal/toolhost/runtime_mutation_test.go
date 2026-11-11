package toolhost_test

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/toolhost"
)

func TestRuntimeKeepsChmodCallableAndDeleteAsCommandOperation(t *testing.T) {
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: configlayout.FindModuleRoot(), Catalog: extpackstest.StockCatalog(t)})
	testutil.FailErr(t, "build native runtime", err)
	if definition, found := rt.Registry.Definition("restore_version"); !found || definition.Handler == nil {
		t.Fatal("restore_version domain tool is unavailable")
	}
	if _, found := rt.Registry.Definition("delete"); found {
		t.Fatal("command-only delete operation is callable")
	}
	if definition, found := rt.Registry.Operation("delete"); !found || definition.Handler != nil || definition.Contract.Owner != "filesystem" {
		t.Fatalf("delete operation=%+v found=%v", definition, found)
	}
}
