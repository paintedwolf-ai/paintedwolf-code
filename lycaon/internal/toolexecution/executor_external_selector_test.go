package toolexecution_test

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcommand"
	"github.com/lycaon/lycaon/internal/toolhost"
)

// The command schema offers the external selector only while the host
// setting leaves it offered.
func TestCommandSchemaOffersExternalOnlyWhenNotHidden(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: content, Desired: extpacks.EmptyDesired()})
	extpacks.SetActive(eff)
	t.Cleanup(extpacks.ClearActive)
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: filepath.Join(filepath.Dir(file), "..", ".."), Catalog: eff})
	testutil.FailErr(t, "toolhost.NewRuntime", err)
	description := func() string {
		metas, err := rt.Executor.Metadata.List(t.Context(), platform.ToolFilter{ProfileID: "implement", ProjectRootCount: 1})
		testutil.FailErr(t, "list tools", err)
		for _, meta := range metas {
			if meta.Name == "command" {
				return meta.Description
			}
		}
		t.Fatal("command not listed")
		return ""
	}
	if !strings.Contains(description(), toolcommand.ExternalSelectorLine) {
		t.Fatal("offered selector missing from the command schema")
	}
	readOnlyMetas, err := rt.Executor.Metadata.List(t.Context(), platform.ToolFilter{ProfileID: "explore_readonly", ProjectRootCount: 1})
	testutil.FailErr(t, "list read-only tools", err)
	for _, meta := range readOnlyMetas {
		if meta.Name == "command" {
			if strings.Contains(meta.Description, "external --") {
				t.Fatalf("read-only command schema must not advertise external selector: %s", meta.Description)
			}
			props, _ := meta.ArgsSchema["properties"].(map[string]any)
			for _, forbidden := range []string{"capability_request", "secret_use", "snapshot_capture", "socks_proxy", "terminal_capture", "verification"} {
				if _, ok := props[forbidden]; ok {
					t.Fatalf("read-only command schema must prune %s: %v", forbidden, props)
				}
			}
		}
	}
	rt.Executor.Metadata.SetCommandExternalHidden(true)
	if got := description(); strings.Contains(got, "external --") {
		t.Fatalf("hidden selector still in the command schema: %s", got)
	}
}
