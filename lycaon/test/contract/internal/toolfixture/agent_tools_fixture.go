package toolfixture

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func ContractToolExecutor(t *testing.T) *tools.DefaultToolExecutor {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	configRoot := filepath.Join(root, "lycaon")
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: configRoot, Catalog: contractcheck.StockCatalog(t)})
	contractcheck.FailErr(t, "toolhost.NewRuntime failed", err)
	applyStockToolSchemas(t, rt)
	hints, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rt.ApplyGuidanceRejects(guidance.NewStaticRejectFormatter(hints))
	WireContractBlockPlane(t, rt, guidance.NewStaticRejectFormatter(hints))
	registerCatalogToolsOnto(t, rt.Registry)
	return rt.Executor
}

func applyStockToolSchemas(t *testing.T, rt *toolhost.Runtime) {
	t.Helper()
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "LoadEffectiveToolSchemas", err)
	rt.Executor.SetToolSchemas(schemas)
}

func LoadBundledAgentRegistry(t *testing.T) orchestration.AgentRegistry {
	t.Helper()
	reg := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), reg))
	if err := orchestration.ValidateGateAgents(reg); err != nil {
		contractcheck.FailErr(t, "validate gate agent references in bundled registry", err)
	}
	return reg
}

func SortedToolNames(ctx context.Context, exec tools.ToolInvoker, profileID string) []string {
	metas := tools.ListToolsForProfile(ctx, exec, platform.ToolFilter{ProfileID: profileID})
	names := make([]string, 0, len(metas))
	for _, meta := range metas {
		names = append(names, meta.Name)
	}
	sort.Strings(names)
	return names
}
