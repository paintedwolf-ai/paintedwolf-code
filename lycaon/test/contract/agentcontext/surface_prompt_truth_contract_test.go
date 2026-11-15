package contract

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// commandEffectVariables maps each prompt variable to the effect it states.
var commandEffectVariables = map[string]toolcontract.CommandEffectClass{
	"profile_command_mutation":       toolcontract.CommandMutation,
	"profile_command_external_exec":  toolcontract.CommandExternalExec,
	"profile_command_network_egress": toolcontract.CommandNetworkEgress,
	"profile_has_terminal_capture":   toolcontract.CommandExternalExec,
}

func allowsEffect(t *testing.T, effects toolcontract.CommandEffects, class toolcontract.CommandEffectClass) bool {
	t.Helper()
	required, err := toolcontract.NewCommandEffects(class)
	contractcheck.FailErr(t, "compile effect", err)
	return effects.Allows(required)
}

// postureProfiles narrows every profile the way the sandbox does for a session
// in the posture.
func postureProfiles(profiles []sandbox.ToolProfile, posture toolcontract.CommandEffects) []sandbox.ToolProfile {
	out := slices.Clone(profiles)
	for i := range out {
		out[i].CommandEffects &= posture
	}
	return out
}

func profileByID(t *testing.T, profiles []sandbox.ToolProfile, id string) sandbox.ToolProfile {
	t.Helper()
	for _, profile := range profiles {
		if profile.ID == id {
			return profile
		}
	}
	t.Fatalf("tool profile %q not shipped", id)
	return sandbox.ToolProfile{}
}

// Every coordinator prompt states exactly the command effects the executor
// admits: the coordinator profile under the session posture, narrowed by the
// surface row.
func TestCoordinatorPromptVariablesStateAdmittedCommandEffects(t *testing.T) {
	surfaces, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load surfaces", err)
	shipped, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	postures, err := sessionprofiles.LoadPostureRegistry()
	contractcheck.FailErr(t, "load postures", err)
	engine := bundledPromptEngine()
	catalog, err := prompts.UnitCatalogForEngine(engine)
	contractcheck.FailErr(t, "unit catalog", err)
	for _, posture := range postures.List() {
		profiles := postureProfiles(shipped, posture.CommandEffects)
		coordinator := profileByID(t, profiles, prompts.CoordinatorProfileID)
		executor := toolexecution.NewExecutor(
			toolprofiles.NewProfilePolicyEngine(sandbox.NewBoundary(sandbox.Config{}, profiles)),
			tools.NewDefaultRegistry(),
			prompts.CoordinatorProfileID,
		)
		for _, surfaceID := range surfaces.SurfaceIDs() {
			t.Run(string(posture.ID)+"/"+surfaceID, func(t *testing.T) {
				row, err := surfaces.Surface(surfaceID)
				contractcheck.FailErr(t, "load surface", err)
				declared, err := toolcontract.NewCommandEffects(row.CommandEffects...)
				contractcheck.FailErr(t, "compile surface effects", err)
				admitted := coordinator.CommandEffects & declared
				programSurface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{
					ProfileID: prompts.CoordinatorProfileID,
					SurfaceID: surfaceID,
				}, nil)
				contractcheck.FailErr(t, "freeze program surface", err)
				execEffects := programSurface.Effects()
				plan, err := prompts.CompileSurfacePlan(surfaceID, 1)
				contractcheck.FailErr(t, "compile surface plan", err)
				reader, err := prompts.NewReader(prompts.ReaderInput{
					Mode:      surface.ExecutionModeFamily(surfaceID),
					ProfileID: prompts.CoordinatorProfileID,
					SurfaceID: surfaceID,
					Plan:      plan,
					Profiles:  profiles,
					RootCount: 1,
				})
				contractcheck.FailErr(t, "new reader", err)
				if reader.Effects() != execEffects || reader.Effects() != admitted {
					t.Fatalf("reader.Effects() = %v, executor = %v, admitted = %v", reader.Effects().Classes(), execEffects.Classes(), admitted.Classes())
				}

				vars := map[string]any{"root_count": 1}
				mergePromptBudgetSkillRoster(t, vars)
				contractcheck.FailErr(t, "merge surface vars", prompts.MergeCoordinatorSurfacePathVars(surfaceID, profiles, vars, prompts.ReaderInput{}))
				contractcheck.FailErr(t, "merge prompt vars", prompts.MergeCoordinatorPromptVars(surfaceID, prompts.ExecutionModePromptTransition{}, "", vars))
				offered, _ := vars["surface_offered"].([]string)
				commandOffered := slices.Contains(offered, "command")
				for name, class := range commandEffectVariables {
					want := commandOffered && allowsEffect(t, admitted, class)
					if got, ok := vars[name].(bool); !ok || got != want {
						t.Errorf("%s = %v, want %v (admitted %v)", name, vars[name], want, admitted.Classes())
					}
				}

				assertCatalogUnitSelection(t, catalog, reader)
				mints, _ := vars["mints"].(map[string]bool)
				if got := mints["tui"]; got != (vars["profile_has_terminal_capture"] == true) {
					t.Errorf("mints[tui] = %v, want %v", got, vars["profile_has_terminal_capture"] == true)
				}
				if vars["profile_command_mutation"] == false && !slices.Contains(offered, "apply_patch") {
					if mints["edit"] || mints["replace"] || mints["rewrite"] || mints["delete"] {
						t.Errorf("read-only command surface claims mutating mints: %v", mints)
					}
				}
			})
		}
	}
}

// Worker prompts state the effects their own profile declares under the posture.
func TestWorkerPromptVariablesStateAdmittedCommandEffects(t *testing.T) {
	shipped, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	postures, err := sessionprofiles.LoadPostureRegistry()
	contractcheck.FailErr(t, "load postures", err)
	catalog, err := prompts.UnitCatalogForEngine(bundledPromptEngine())
	contractcheck.FailErr(t, "unit catalog", err)
	for _, posture := range postures.List() {
		profiles := postureProfiles(shipped, posture.CommandEffects)
		for _, profile := range profiles {
			if profile.ID == prompts.CoordinatorProfileID {
				continue
			}
			t.Run(string(posture.ID)+"/"+profile.ID, func(t *testing.T) {
				executor := toolexecution.NewExecutor(
					toolprofiles.NewProfilePolicyEngine(sandbox.NewBoundary(sandbox.Config{}, profiles)),
					tools.NewDefaultRegistry(),
					profile.ID,
				)
				programSurface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{
					ProfileID: profile.ID,
				}, nil)
				contractcheck.FailErr(t, "freeze worker program surface", err)
				reader, err := prompts.NewReader(prompts.ReaderInput{
					ProfileID: profile.ID,
					Profiles:  profiles,
					RootCount: 1,
					WebSearch: true,
				})
				contractcheck.FailErr(t, "new worker reader", err)
				if reader.Effects() != programSurface.Effects() || reader.Effects() != profile.CommandEffects {
					t.Fatalf("worker reader.Effects() = %v, executor = %v, want %v", reader.Effects().Classes(), programSurface.Effects().Classes(), profile.CommandEffects.Classes())
				}
				assertCatalogUnitSelection(t, catalog, reader)
				vars := map[string]any{}
				contractcheck.FailErr(t, "merge profile vars", prompts.MergeAgentToolSurfaceVars(profile.ID, nil, nil, prompts.ReaderInput{}, vars, profiles))
				commandOffered, _ := vars["profile_has_command"].(bool)
				for name, class := range commandEffectVariables {
					want := commandOffered && allowsEffect(t, profile.CommandEffects, class)
					if got, ok := vars[name].(bool); !ok || got != want {
						t.Errorf("%s = %v, want %v (declared %v)", name, vars[name], want, profile.CommandEffects.Classes())
					}
				}
			})
		}
	}
}

func assertCatalogUnitSelection(t *testing.T, catalog promptunit.Catalog, reader prompts.Reader) {
	t.Helper()
	sel := reader.Selection()
	rendered := catalog.Rendered(sel)
	selected := make(map[string]bool)
	for _, units := range rendered {
		for _, u := range units {
			selected[u.ID] = true
		}
	}
	for _, u := range catalog.Units() {
		want := u.HostsFor(sel.Host) && u.ModeFits(sel.Mode) && (len(u.Attaches) == 0 || u.AttachedTo(sel.Held))
		if got := selected[u.ID]; got != want {
			t.Errorf("unit %s selected = %v, want %v (host=%s mode=%s attaches=%v)", u.ID, got, want, sel.Host, sel.Mode, u.Attaches)
		}
	}
}
