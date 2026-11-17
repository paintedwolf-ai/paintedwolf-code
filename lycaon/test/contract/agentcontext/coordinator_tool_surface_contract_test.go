package contract

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorToolSurfaceSecretsRemainRequestableWithOutboundExecution(t *testing.T) {
	t.Parallel()
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: toolcontract.SurfaceImplementInvestigate}, 1)
	contractcheck.FailErr(t, "CompileToolPlan", err)
	deferred := plan.DeferredNames()
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load actual command effect declarations", err)
	network, err := toolcontract.NewCommandEffects(toolcontract.CommandNetworkEgress)
	contractcheck.FailErr(t, "declare network effect", err)
	for _, id := range []string{toolcontract.SurfaceImplementInvestigate, "implement_overlay_promote"} {
		row, err := catalog.Surface(id)
		contractcheck.FailErr(t, "load outbound surface", err)
		effects, err := toolcontract.NewCommandEffects(row.CommandEffects...)
		contractcheck.FailErr(t, "compile outbound effects", err)
		if !contractcheck.ContainsString(row.FloorTools(), "command") || !effects.Allows(network) {
			t.Fatalf("%s must provide native outbound command capability", id)
		}
	}

	promoteDeferred, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: "implement_overlay_promote"}, 1)
	contractcheck.FailErr(t, "CompileToolPlan", err)
	for _, name := range []string{"secret_generate", "secret_list", "secret_revoke"} {
		if !contractcheck.ContainsString(deferred, name) {
			t.Fatalf("implement_investigate missing deferred outbound capability %q", name)
		}
		if !contractcheck.ContainsString(promoteDeferred.DeferredNames(), name) {
			t.Fatalf("implement_overlay_promote missing deferred outbound capability %q", name)
		}
	}
}

func TestCoordinatorToolSurface_managedSecretLifecycleFollowsMutationCapability(t *testing.T) {
	t.Parallel()
	for surfaceID := range loadImplementSurfaces(t) {
		plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, 1)
		contractcheck.FailErr(t, "compile tool plan for "+surfaceID, err)
		available := plan.AddressableNames()
		reader, err := prompts.DeclaredReader(nil, surfaceID)
		contractcheck.FailErr(t, "DeclaredReader for "+surfaceID, err)
		if capable, _ := reader.Facts()["profile_mutation_capable"].(bool); !capable {
			continue
		}
		if !contractcheck.ContainsString(available, "skills_read") {
			t.Errorf("mutation-capable coordinator surface %q cannot read the managed-secret procedure", surfaceID)
		}
		for _, name := range managedSecretLifecycleTools {
			if !contractcheck.ContainsString(available, name) {
				t.Errorf("mutation-capable coordinator surface %q is missing %q", surfaceID, name)
			}
		}
	}
}
