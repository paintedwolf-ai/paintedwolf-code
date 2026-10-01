package toolhost

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestBuildNativeRegistryConstructsSummarizeWithoutProviderServices(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{
		config.NativeTools: "native:\n  filesystem:\n    - summarize\n",
	})
	nativeConfig, err := nativemanifest.Load()
	testutil.FailErr(t, "load native config", err)
	toolSchemas, _, err := extpacks.LoadEffectiveToolSchemas(extpackstest.StockCatalog(t))
	testutil.FailErr(t, "load tool schemas", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{{
		ID: tools.DefaultToolProfileID, Tools: map[string]bool{"summarize": true},
	}})

	registry, mutationTools, err := buildNativeRegistry(buildDeps{
		boundary: boundary, nativeConfig: nativeConfig, toolSchemas: toolSchemas,
	})
	testutil.FailErr(t, "build native registry", err)
	if mutationTools.summarize == nil {
		t.Fatal("native summarize tool was not constructed")
	}
	for _, metadata := range registry.List() {
		if metadata.Name == "summarize" {
			return
		}
	}
	t.Fatal("native summarize tool was not registered")
}
