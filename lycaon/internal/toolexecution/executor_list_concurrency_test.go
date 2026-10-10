package toolexecution_test

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolhost"
)

// TestExecutorListConcurrentCommandSchema checks immutable canonical schema reads.
func TestExecutorListConcurrentCommandSchema(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   content,
		Desired: extpacks.EmptyDesired(),
	})
	extpacks.SetActive(eff)
	t.Cleanup(extpacks.ClearActive)

	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: root, Catalog: eff})
	testutil.FailErr(t, "toolhost.NewRuntime", err)

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		rootCount := 1
		if i%2 == 0 {
			rootCount = 2
		}
		wg.Add(1)
		go func(rc int) {
			defer wg.Done()
			metas, listErr := rt.Executor.Metadata.List(ctx, platform.ToolFilter{
				ProfileID:        "implement",
				ProjectRootCount: rc,
			})
			if listErr != nil {
				t.Errorf("list tools at root count %d: %v", rc, listErr)
				return
			}
			found := false
			for _, meta := range metas {
				if meta.Name != "command" {
					continue
				}
				found = true
				if !strings.Contains(meta.Description, "`$()`/backticks reject") {
					t.Errorf("command description omits substitution guidance at root count %d", rc)
				}
				props, _ := meta.ArgsSchema["properties"].(map[string]any)
				if _, hasRoot := props["root"]; hasRoot {
					t.Errorf("command schema advertises root at root count %d — advertised schema must stay canonical", rc)
				}
				if _, hasCwd := props["cwd"]; !hasCwd {
					t.Errorf("command schema missing cwd at root count %d", rc)
				}
			}
			if !found {
				t.Errorf("command missing from tool list at root count %d", rc)
			}
		}(rootCount)
	}
	wg.Wait()
}
