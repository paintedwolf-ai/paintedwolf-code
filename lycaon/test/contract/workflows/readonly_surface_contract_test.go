package contract

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

// The read-only postures are the ones the flow table dispatches on, so the set
// derives from the table. Both forbidden axes are generated from
// native-tools.yaml, so a tool added there is covered without editing this test.
func TestReadOnlyPostureSurfacesCarryNoMutatingTool(t *testing.T) {
	t.Parallel()
	table, err := surface.ShippedCoordinatorFlowTable()
	contractcheck.FailErr(t, "load coordinator flow table", err)
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load coordinator surfaces", err)
	manifests, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "load workflow catalog", err)

	postures := readOnlyPostures(table)
	if len(postures) == 0 {
		t.Fatal("no read-only posture dispatch in the coordinator flow table")
	}
	for _, posture := range postures {
		reachable := flowReachableSurfaces(table, posture)
		for id := range manifestBoundSurfaces(manifests, posture) {
			reachable[id] = struct{}{}
		}
		if len(reachable) == 0 {
			t.Errorf("posture %q dispatches read-only but reaches no surface", posture)
		}
		for _, id := range sortedKeys(reachable) {
			row, err := catalog.Surface(id)
			contractcheck.FailErr(t, "load surface "+id, err)
			for _, tool := range surfaceTools(row) {
				if reason := forbiddenReadOnlyTool(row, tool); reason != "" {
					t.Errorf("posture %q reaches surface %q, which offers %q (%s)", posture, id, tool, reason)
				}
			}
			verifyReadOnlyProgramSurface(t, platform.ToolFilter{ProfileID: toolcontract.CoordinatorProfileID, SurfaceID: id})
		}
	}
	for _, profileID := range []string{"worker_readonly", "explore_readonly", "plan_review_readonly"} {
		verifyReadOnlyProgramSurface(t, platform.ToolFilter{ProfileID: profileID})
	}
}

func verifyReadOnlyProgramSurface(t *testing.T, filter platform.ToolFilter) {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load shipped tool profiles", err)
	boundary := sandbox.NewBoundary(sandbox.Config{}, profiles)
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), nil)
	registry := tools.NewDefaultRegistry()
	names := []string{"read", "find", "extract_archive", "git_status", "git_diff", "git_log", "write", "git_commit", "http_request"}
	defs := make(map[string]toolcontract.OperationDefinition, len(names))
	active := make([]toolcontract.OperationDefinition, 0, len(names))
	for _, name := range names {
		contract, ok := toolcontract.Lookup(name)
		if !ok {
			t.Fatalf("missing contract for %s", name)
		}
		contractcheck.FailErr(t, "register "+name, registry.RegisterDefinition(tools.Definition{
			Meta:     tools.ToolMeta{Name: name, ArgsSchema: map[string]any{"type": "object"}},
			Contract: contract,
			Handler: func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				t.Fatalf("read-only surface admission invoked handler for %s", name)
				return "", nil
			},
		}))
		def, _ := registry.Definition(name)
		defs[name] = def.Operation
		active = append(active, def.Operation)
	}
	executor := toolexecution.NewExecutor(policy, registry, filter.ProfileID)
	frozen, err := executor.Metadata.FreezeProgramSurface(t.Context(), filter, active)
	contractcheck.FailErr(t, "freeze program surface "+filter.ProfileID+"/"+filter.SurfaceID, err)
	invocation := toolcontract.OperationInvocation{
		Kind:          toolcontract.ProgramChildInvocation,
		Identity:      toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: "child"},
		ActorID:       "principal",
		SessionID:     "session",
		Scope:         toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope},
		SourceUnitID:  "source",
		Span:          toolcontract.SourceSpan{StartByte: 1, EndByte: 4, Line: 1, Column: 1},
		CatalogDigest: toolcontract.LanguageCatalogDigest(),
	}
	for _, okCase := range []struct {
		tool, command, mode string
	}{
		{"read", "cat", "always"},
		{"find", "find", "default-predicates-print"},
		{"extract_archive", "tar", "listing-or-stream-decode"},
		{"git_status", "git status", "query"},
		{"git_diff", "git diff", "query"},
		{"git_log", "git log", "query"},
	} {
		if err := frozen.ValidateBound(defs[okCase.tool], invocation, okCase.command, okCase.mode, nil); err != nil {
			t.Errorf("%s/%s rejected read-only %s (%s): %v", filter.ProfileID, filter.SurfaceID, okCase.command, okCase.mode, err)
		}
	}
	externalExec, err := toolcontract.NewCommandEffects(toolcontract.CommandExternalExec)
	contractcheck.FailErr(t, "compile external_exec effect", err)
	for _, badCase := range []struct {
		tool, command, mode string
		child               *toolcontract.CommandEffects
	}{
		{"write", "tee", "each-target", nil},
		{"find", "find", "-delete", nil},
		{"find", "find", "-exec", &externalExec},
		{"extract_archive", "tar", "create-extract-compress-decompress-to-path", nil},
		{"git_commit", "git commit", "mutation-network", nil},
		{"http_request", "curl", "request", nil},
	} {
		if err := frozen.ValidateBound(defs[badCase.tool], invocation, badCase.command, badCase.mode, badCase.child); err == nil {
			t.Errorf("%s/%s admitted non-read-only %s (%s)", filter.ProfileID, filter.SurfaceID, badCase.command, badCase.mode)
		}
	}
}

// forbiddenReadOnlyTool names why a tool cannot sit on a read-only surface.
func forbiddenReadOnlyTool(row surfacecatalog.Surface, tool string) string {
	switch {
	case settings.IsPathMutatingTool(tool):
		return "mutates a path its arguments declare"
	case settings.IsCommandToolName(tool):
		readOnly, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
		if err != nil {
			return err.Error()
		}
		effects, err := toolcontract.NewCommandEffects(row.CommandEffects...)
		if err != nil || effects != readOnly {
			return "runs a program with non-read-only command effects"
		}
		return ""
	default:
		return ""
	}
}

func readOnlyPostures(table surface.FlowTable) []string {
	seen := map[string]struct{}{}
	collect := func(out surface.FlowOutput) {
		if strings.TrimSpace(out.DispatchOn) != surface.FactPosture {
			return
		}
		for posture := range out.Cases {
			seen[strings.TrimSpace(posture)] = struct{}{}
		}
	}
	for _, rule := range table.Rules {
		collect(rule.Out)
	}
	collect(table.Default)
	return sortedKeys(seen)
}

// flowReachableSurfaces resolves every row's destination at one posture.
// use_fact rows resolve to a manifest-bound surface, which the manifest pass
// covers directly.
func flowReachableSurfaces(table surface.FlowTable, posture string) map[string]struct{} {
	out := map[string]struct{}{}
	add := func(o surface.FlowOutput) {
		if id := strings.TrimSpace(o.SurfaceID); id != "" {
			out[id] = struct{}{}
			return
		}
		if strings.TrimSpace(o.DispatchOn) == "" {
			return
		}
		if o.DispatchOn == surface.FactPosture {
			if id, ok := o.Cases[posture]; ok {
				out[strings.TrimSpace(id)] = struct{}{}
				return
			}
		}
		if id := strings.TrimSpace(o.Default); id != "" {
			out[id] = struct{}{}
		}
	}
	for _, rule := range table.Rules {
		add(rule.Out)
	}
	add(table.Default)
	return out
}

func manifestBoundSurfaces(manifests map[string]workflowdef.Manifest, posture string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, m := range manifests {
		if !manifestRunsAtPosture(m, posture) {
			continue
		}
		for _, phase := range m.PhaseDefs {
			if id := strings.TrimSpace(phase.CoordinatorSurface); id != "" {
				out[id] = struct{}{}
			}
		}
	}
	return out
}

func manifestRunsAtPosture(m workflowdef.Manifest, posture string) bool {
	if strings.TrimSpace(m.InitialPosture) == posture {
		return true
	}
	for _, phase := range m.PhaseDefs {
		if strings.TrimSpace(phase.OnEnter.SetPosture) == posture {
			return true
		}
	}
	return false
}

func surfaceTools(row surfacecatalog.Surface) []string {
	return append(toolcontract.ExpandFamilies(row.Floor), row.LoadableTools()...)
}

func sortedKeys(in map[string]struct{}) []string {
	out := make([]string, 0, len(in))
	for k := range in {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
