package toolhost_test

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/toolhost"
)

func TestRuntimeRegistersChmodAndDelete(t *testing.T) {
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: configlayout.FindModuleRoot(), Catalog: extpackstest.StockCatalog(t)})
	testutil.FailErr(t, "toolhost.NewRuntime failed", err)
	names := make([]string, 0, 2)
	for _, meta := range rt.Registry.List() {
		if meta.Name == "chmod" || meta.Name == "delete" {
			names = append(names, meta.Name)
		}
	}
	if !strings.Contains(strings.Join(names, ","), "chmod") || !strings.Contains(strings.Join(names, ","), "delete") {
		t.Fatalf("registry tools = %v want chmod and delete", names)
	}
}
