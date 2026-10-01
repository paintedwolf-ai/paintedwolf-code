package contract

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// phaseExitChannel names a tool required to leave a phase.
type phaseExitChannel struct {
	tool string
	why  string
}

// phaseExitChannels returns the tools this phase's declared leave paths need.
func phaseExitChannels(m workflowdef.Manifest, phase workflowdef.PhaseDef) []phaseExitChannel {
	var out []phaseExitChannel
	if phase.Terminal {
		return nil
	}
	if workflowdef.EffectiveAdvancePolicy(m, phase) == workflowdef.AdvanceWhenGateMetCoordinator {
		out = append(out, phaseExitChannel{"workflow_advance", "advance.when_gate_met: coordinator"})
	}
	if phase.ReviewLoop != nil {
		out = append(out, phaseExitChannel{"submit_verdict", "review_loop verdict"})
	}
	for _, gate := range phase.Gates {
		if strings.HasPrefix(gate, "hitl_consulted") {
			out = append(out, phaseExitChannel{"ask_user", "gate " + gate})
			break
		}
	}
	for _, tr := range phase.Transitions {
		for _, actor := range tr.Actors {
			if actor == workflowdef.TransitionActorCoordinator {
				out = append(out, phaseExitChannel{"workflow_transition", "coordinator transition " + tr.ID})
				break
			}
		}
	}
	return out
}

// resolvePhaseSurface returns the explicit or profile binding.
func resolvePhaseSurface(t *testing.T, m workflowdef.Manifest, phase workflowdef.PhaseDef) string {
	t.Helper()
	if s := strings.TrimSpace(phase.CoordinatorSurface); s != "" {
		return s
	}
	runCtx := surface.EnrichRunContextForWorkflow(api.CoordinatorRunContext{
		WorkflowID:     m.ID,
		CurrentPhase:   phase.ID,
		SurfaceProfile: m.SurfaceProfile,
	}, contractcheck.RepoRoot(t)+"/lycaon")
	return strings.TrimSpace(runCtx.PhaseCoordinatorSurface)
}

func TestWorkflowPhaseExitChannelsAreOnTheBoundSurface(t *testing.T) {
	t.Parallel()
	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "RegistryFromDirs", err)
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "CompileToolPlans", err)

	var violations []string
	for key, m := range reg.All() {
		for _, phase := range m.PhaseDefs {
			channels := phaseExitChannels(m, phase)
			if len(channels) == 0 {
				continue
			}
			surfaceID := resolvePhaseSurface(t, m, phase)
			if surfaceID == "" {
				// Surface invariants cover dynamic bindings.
				continue
			}
			plan, ok := plans[surfaceID]
			if !ok {
				continue
			}
			wire := plan.AddressableNames()
			for _, ch := range channels {
				if contractcheck.ContainsString(wire, ch.tool) {
					continue
				}
				violations = append(violations,
					key+" phase "+phase.ID+" → surface "+surfaceID+
						" needs "+ch.tool+" ("+ch.why+")")
			}
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("workflow phases whose declared leave channel is not callable on the bound surface:\n  %s\n"+
			"Add the tool to that surface in coordinator-surfaces.yaml, or change the phase's leave policy.",
			strings.Join(violations, "\n  "))
	}
}

// phaseExitImperativeRE captures non-negated tool-call instructions.
var phaseExitImperativeRE = regexp.MustCompile("((?i:do not |never ))?call `([a-z_]+)`")

// Rendered exit instructions name callable tools.
func TestWorkflowPhaseExitStepsOnlyNameCallableTools(t *testing.T) {
	t.Parallel()
	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "RegistryFromDirs", err)
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "CompileToolPlans", err)

	// The profile distinguishes tools from gate and phase IDs.
	profile := loadCoordinatorProfileTools(t)

	var violations []string
	for key, m := range reg.All() {
		for _, phase := range m.PhaseDefs {
			surfaceID := resolvePhaseSurface(t, m, phase)
			if surfaceID == "" {
				continue
			}
			plan, ok := plans[surfaceID]
			if !ok {
				continue
			}
			wire := plan.AddressableNames()
			view := workflow.ProjectPhaseExit(m, phase, nil, nil)
			seen := map[string]bool{}
			for _, step := range strings.Split(renderPhaseExitBlock(t, view), "\n") {
				for _, match := range phaseExitImperativeRE.FindAllStringSubmatch(step, -1) {
					if match[1] != "" {
						continue
					}
					name := match[2]
					if seen[name] || !coordinatorProfileGrantsTool(profile, name) || contractcheck.ContainsString(wire, name) {
						continue
					}
					seen[name] = true
					violations = append(violations,
						key+" phase "+phase.ID+" → surface "+surfaceID+
							" exit step says call `"+name+"`, which is off that surface")
				}
			}
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("phase-exit steps naming a tool the bound surface cannot call:\n  %s",
			strings.Join(violations, "\n  "))
	}
}
