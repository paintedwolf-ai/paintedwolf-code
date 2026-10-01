package definition

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseManifestYAMLSurfaceBindingFields(t *testing.T) {
	m, err := ParseManifestYAML([]byte(`
id: design-doc
version: 1.0.0
surface_profile: plan
phases:
  - id: intake
    activity_label: Test phase
    coordinator_surface: plan_stub
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [plan-stub]
`))
	testutil.FailErr(t, "ParseManifestYAML", err)
	if m.SurfaceProfile != "plan" {
		t.Fatalf("surface_profile = %q", m.SurfaceProfile)
	}
	stub, ok := m.PhaseByID("intake")
	if !ok {
		t.Fatal("missing intake phase")
	}
	if stub.CoordinatorSurface != "plan_stub" || stub.SurfaceTemplate != "agents/coordinator-surface-plan.md" {
		t.Fatalf("intake binding = %+v", stub)
	}
	if len(stub.ModeRefs) != 1 || stub.ModeRefs[0] != "plan-stub" {
		t.Fatalf("mode_refs = %v", stub.ModeRefs)
	}
}

func TestParseManifestYAMLRejectsUnknownCoordinatorSurface(t *testing.T) {
	_, err := ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
phases:
  - id: work
    activity_label: Test phase
    coordinator_surface: not_a_real_surface
    surface_template: agents/coordinator-surface-plan.md
`))
	if err == nil {
		t.Fatal("expected unknown coordinator_surface error")
	}
	if !strings.Contains(err.Error(), "unknown coordinator_surface") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseManifestYAMLRejectsUnknownSurfaceProfile(t *testing.T) {
	_, err := ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
surface_profile: missing-profile
phases:
  - id: work
    activity_label: Test phase
`))
	if err == nil {
		t.Fatal("expected unknown surface_profile error")
	}
	if !strings.Contains(err.Error(), "unknown surface_profile") {
		t.Fatalf("err = %v", err)
	}
}

func TestMarshalManifestYAMLSurfaceBindingRoundTrip(t *testing.T) {
	original, err := ParseManifestYAML([]byte(`
id: design-doc
version: 1.0.0
surface_profile: plan
phases:
  - id: intake
    activity_label: Test phase
    coordinator_surface: plan_stub
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [plan-stub]
`))
	testutil.FailErr(t, "ParseManifestYAML", err)
	raw, err := MarshalManifestYAML(original)
	testutil.FailErr(t, "MarshalManifestYAML", err)
	reloaded, err := ParseManifestYAML([]byte(raw))
	testutil.FailErr(t, "ParseManifestYAML reload", err)
	if reloaded.SurfaceProfile != original.SurfaceProfile {
		t.Fatalf("surface_profile = %q want %q", reloaded.SurfaceProfile, original.SurfaceProfile)
	}
	intake, ok := reloaded.PhaseByID("intake")
	if !ok {
		t.Fatal("missing intake")
	}
	want, _ := original.PhaseByID("intake")
	if intake.CoordinatorSurface != want.CoordinatorSurface || intake.SurfaceTemplate != want.SurfaceTemplate {
		t.Fatalf("intake = %+v want %+v", intake, want)
	}
	if len(intake.ModeRefs) != len(want.ModeRefs) || intake.ModeRefs[0] != want.ModeRefs[0] {
		t.Fatalf("mode_refs = %v want %v", intake.ModeRefs, want.ModeRefs)
	}
}

func TestResolveManifestExtendsSurfaceProfileInheritance(t *testing.T) {
	parent, err := ParseManifestYAML([]byte(`
id: plan
version: 1.0.0
surface_profile: plan
phases:
  - id: intake
    activity_label: Test phase
    coordinator_surface: plan_stub
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [plan-stub]
  - id: research
    activity_label: Test phase
    coordinator_surface: plan_research
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [plan-research]
`))
	testutil.FailErr(t, "parse parent", err)
	child, err := ParseManifestYAML([]byte(`
id: child-plan
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: intake
    activity_label: Test phase
    next: approve
`))
	testutil.FailErr(t, "parse child", err)
	catalog := map[string]Manifest{"plan@1.0.0": parent}
	merged, err := ResolveManifestChain(child, catalog)
	testutil.FailErr(t, "ResolveManifestChain", err)
	if merged.SurfaceProfile != "plan" {
		t.Fatalf("surface_profile = %q want plan", merged.SurfaceProfile)
	}
	intake, ok := merged.PhaseByID("intake")
	if !ok {
		t.Fatal("missing intake")
	}
	if intake.CoordinatorSurface != "plan_stub" || intake.SurfaceTemplate != "agents/coordinator-surface-plan.md" {
		t.Fatalf("intake binding = %+v", intake)
	}
	if len(intake.ModeRefs) != 1 || intake.ModeRefs[0] != "plan-stub" {
		t.Fatalf("mode_refs = %v", intake.ModeRefs)
	}
}

func TestResolveSurfaceBindingPlanProfile(t *testing.T) {
	m := Manifest{
		ID:             "plan",
		Version:        "1.0.0",
		SurfaceProfile: "plan",
		PhaseDefs: []PhaseDef{
			{ID: "research"},
		},
	}
	root := filepath.Join("..", "..", "..")
	binding, err := ResolveSurfaceBinding(m, "research", "plan@1.0.0", root)
	testutil.FailErr(t, "ResolveSurfaceBinding", err)
	if binding.CoordinatorSurface != "plan_research" {
		t.Fatalf("surface = %q", binding.CoordinatorSurface)
	}
	if binding.SurfaceTemplate != "agents/coordinator-surface-plan.md" {
		t.Fatalf("template = %q", binding.SurfaceTemplate)
	}
	if len(binding.ModeRefs) != 1 || binding.ModeRefs[0] != "plan-research" {
		t.Fatalf("mode_refs = %v", binding.ModeRefs)
	}
	if binding.WorkflowInvestigateEligible {
		t.Fatal("plan profile must not be investigate eligible")
	}
}

func TestResolveSurfaceBindingEmptyWorkflowUsesImplementProfile(t *testing.T) {
	m := Manifest{ID: "implement", Version: "1.0.0"}
	root := filepath.Join("..", "..", "..")
	binding, err := ResolveSurfaceBinding(m, "work", "", root)
	testutil.FailErr(t, "ResolveSurfaceBinding", err)
	if !binding.WorkflowInvestigateEligible {
		t.Fatal("empty workflow id must resolve implement investigate_eligible")
	}
	if binding.CoordinatorSurface != "" {
		t.Fatalf("unexpected surface pin = %q", binding.CoordinatorSurface)
	}
}
