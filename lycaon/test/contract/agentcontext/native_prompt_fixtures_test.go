//go:build budgets

package contract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func writeNativePromptFixtures(t *testing.T, root string, registry *PromptBudgetRegistry) {
	t.Helper()
	dir := os.Getenv(sizebudget.ReportDirEnv)
	if dir == "" {
		return
	}
	dir = filepath.Join(dir, "native-requests")
	contractcheck.FailErr(t, "create native request directory", os.MkdirAll(dir, 0o755))
	metas := nativeFixtureTools(t)
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile fixture surfaces", err)
	engine := contractPersonaEngine(t)
	renderer := prompts.NewInjectRenderer(engine)
	var injects []string
	for _, spec := range PromptBudgetInjectMatrix {
		if slices.Contains(spec.Hosts, promptunit.HostCoordinator) {
			injects = append(injects, measureCoordinatorInjectBlock(t, filepath.Join(root, "lycaon"), renderer, spec))
		}
	}
	for _, row := range registry.Tripartite {
		forced := ""
		if row.SurfaceID == toolcontract.SurfaceImplementInvestigate {
			forced = row.SurfaceID
		}
		system := renderCoordinatorTripartiteForRunContext(t, root, row.RunCtx, row.Sess, row.UserPrompt, row.History, forced, row.State)
		plan, ok := plans[row.SurfaceID]
		if !ok || filepath.Base(row.Name) != row.Name || row.Name == "." || row.Name == ".." {
			t.Fatalf("invalid native request fixture %q", row.Name)
		}
		names := slices.Concat(plan.ImmediateNames(), plan.DeferredNames())
		definitions := make([]map[string]any, 0, len(names))
		for _, meta := range metas {
			if slices.Contains(names, meta.Name) {
				meta = tools.ProjectProviderToolMeta(meta)
				definitions = append(definitions, map[string]any{"type": "function", "function": toolSurfaceWireView{
					Name: meta.Name, Description: meta.Description, Parameters: meta.ArgsSchema,
				}})
			}
		}
		request := map[string]any{"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": strings.Join(injects, "\n\n")},
			{"role": "user", "content": row.UserPrompt},
		}, "tools": definitions}
		raw, err := json.Marshal(request)
		contractcheck.FailErr(t, "encode native request", err)
		contractcheck.FailErr(t, "write native request", os.WriteFile(filepath.Join(dir, row.Name+".json"), raw, 0o644))
	}
}

func nativeFixtureTools(t *testing.T) []tools.ToolMeta {
	t.Helper()
	registry := toolfixture.ContractServeBootRegistry(t)
	registerContractWorkerSurfaceTools(t, registry)
	config, err := sandbox.LoadConfig()
	contractcheck.FailErr(t, "load fixture sandbox", err)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load fixture profiles", err)
	boundary := sandbox.NewBoundary(config, profiles)
	executor := toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), registry, toolprofiles.DefaultToolProfileID)
	metas, err := executor.Metadata.List(context.Background(), platform.ToolFilter{ProfileID: "coordinator"})
	contractcheck.FailErr(t, "list fixture tools", err)
	return withLargestPhaseVerdictCall(t, metas)
}
