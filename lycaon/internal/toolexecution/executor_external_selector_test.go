package toolexecution_test

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
)

type fixedEffectsPolicy struct {
	effects toolcontract.CommandEffects
}

func (p fixedEffectsPolicy) Evaluate(context.Context, platform.PolicyContext) (*platform.PolicyDecision, error) {
	return &platform.PolicyDecision{Allowed: true}, nil
}

func (p fixedEffectsPolicy) CommandEffects(context.Context, string) (toolcontract.CommandEffects, error) {
	return p.effects, nil
}

var prunedCommandArgs = []string{
	"capability_request",
	"secret_use",
	"snapshot_capture",
	"socks_proxy",
	"terminal_capture",
	"verification",
}

func effectsOf(t *testing.T, classes ...toolcontract.CommandEffectClass) toolcontract.CommandEffects {
	t.Helper()
	eff, err := toolcontract.NewCommandEffects(classes...)
	testutil.FailErr(t, "NewCommandEffects", err)
	return eff
}

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
	parts := func() []string {
		metas, err := rt.Executor.Metadata.List(t.Context(), platform.ToolFilter{ProfileID: "implement", ProjectRootCount: 1})
		testutil.FailErr(t, "list tools", err)
		for _, meta := range metas {
			if meta.Name == "command" {
				return meta.DescriptionParts
			}
		}
		t.Fatal("command not listed")
		return nil
	}
	if !slices.Contains(parts(), "external_exec_selector") {
		t.Fatalf("offered selector missing from DescriptionParts: %v", parts())
	}
	readOnlyMetas, err := rt.Executor.Metadata.List(t.Context(), platform.ToolFilter{ProfileID: "explore_readonly", ProjectRootCount: 1})
	testutil.FailErr(t, "list read-only tools", err)
	for _, meta := range readOnlyMetas {
		if meta.Name == "command" {
			if slices.Contains(meta.DescriptionParts, "external_exec_selector") {
				t.Fatalf("read-only command schema must not include external_exec_selector: %v", meta.DescriptionParts)
			}
			props, _ := meta.ArgsSchema["properties"].(map[string]any)
			for _, forbidden := range prunedCommandArgs {
				if _, ok := props[forbidden]; ok {
					t.Fatalf("read-only command schema must prune %s: %v", forbidden, props)
				}
			}
		}
	}
	rt.Executor.Metadata.SetCommandExternalHidden(true)
	if got := parts(); slices.Contains(got, "external_exec_selector") {
		t.Fatalf("hidden selector still in DescriptionParts: %v", got)
	}
}

func TestCommandMetaProjectionMatchesAdmittedEffects(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	schemaDir := filepath.Join(filepath.Dir(file), "..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas")
	schemas, err := toolschema.LoadSchemaDir(schemaDir)
	testutil.FailErr(t, "LoadSchemaDir", err)
	reg, err := tools.NewCatalogRegistry(schemas)
	testutil.FailErr(t, "NewCatalogRegistry", err)
	testutil.FailErr(t, "Register command", reg.Register("command", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "", nil
	}))

	cases := []struct {
		name           string
		effects        toolcontract.CommandEffects
		surfaceID      string
		externalHidden bool
		wantParts      []string
		wantArgs       bool
	}{
		{
			name:      "none",
			effects:   0,
			wantParts: []string{"base"},
			wantArgs:  false,
		},
		{
			name:      "read_only",
			effects:   effectsOf(t, toolcontract.CommandReadOnly),
			wantParts: []string{"base"},
			wantArgs:  false,
		},
		{
			name:      "mutation_only",
			effects:   effectsOf(t, toolcontract.CommandMutation),
			wantParts: []string{"base", "mutation"},
			wantArgs:  false,
		},
		{
			name:      "external_exec_only",
			effects:   effectsOf(t, toolcontract.CommandExternalExec),
			wantParts: []string{"base", "external_exec", "external_exec_selector"},
			wantArgs:  true,
		},
		{
			name:           "external_exec_selector_hidden",
			effects:        effectsOf(t, toolcontract.CommandExternalExec),
			externalHidden: true,
			wantParts:      []string{"base", "external_exec"},
			wantArgs:       true,
		},
		{
			name:      "network_egress_only",
			effects:   effectsOf(t, toolcontract.CommandNetworkEgress),
			wantParts: []string{"base", "network_egress"},
			wantArgs:  false,
		},
		{
			name:    "full",
			effects: effectsOf(t, toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress),
			wantParts: []string{
				"base",
				"mutation",
				"external_exec",
				"external_exec_selector",
				"network_egress",
			},
			wantArgs: true,
		},
		{
			name:           "full_selector_hidden",
			effects:        effectsOf(t, toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress),
			externalHidden: true,
			wantParts: []string{
				"base",
				"mutation",
				"external_exec",
				"network_egress",
			},
			wantArgs: true,
		},
		{
			name:      "surface_intersection_narrows_full_profile",
			effects:   effectsOf(t, toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress),
			surfaceID: "observe_investigate",
			wantParts: []string{"base"},
			wantArgs:  false,
		},
		{
			name:      "unknown_surface_claims_no_effects",
			effects:   effectsOf(t, toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress),
			surfaceID: "unknown_surface",
			wantParts: []string{"base"},
			wantArgs:  false,
		},
	}

	t.Run("nil_policy_claims_no_effects", func(t *testing.T) {
		exec := toolexecution.NewExecutor(nil, reg, "implement")
		metas, err := exec.Metadata.List(t.Context(), platform.ToolFilter{ProfileID: "implement"})
		testutil.FailErr(t, "Metadata.List", err)
		if len(metas) != 1 || !slices.Equal(metas[0].DescriptionParts, []string{"base"}) {
			t.Fatalf("nil policy DescriptionParts = %v, want [base]", metas)
		}
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := toolexecution.NewExecutor(fixedEffectsPolicy{effects: tc.effects}, reg, "implement")
			exec.Metadata.SetCommandExternalHidden(tc.externalHidden)
			metas, err := exec.Metadata.List(t.Context(), platform.ToolFilter{
				ProfileID: "implement",
				SurfaceID: tc.surfaceID,
			})
			testutil.FailErr(t, "Metadata.List", err)
			if len(metas) != 1 || metas[0].Name != "command" {
				t.Fatalf("listed = %v, want [command]", metas)
			}
			got := metas[0]
			if !slices.Equal(got.DescriptionParts, tc.wantParts) {
				t.Fatalf("DescriptionParts = %v, want %v", got.DescriptionParts, tc.wantParts)
			}
			props, _ := got.ArgsSchema["properties"].(map[string]any)
			for _, arg := range prunedCommandArgs {
				_, present := props[arg]
				if present != tc.wantArgs {
					t.Errorf("argument %q present = %v, want %v", arg, present, tc.wantArgs)
				}
			}
		})
	}
}
