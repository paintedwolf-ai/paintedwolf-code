package contract

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/toolcontract"
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
	for _, posture := range postures.List() {
		profiles := postureProfiles(shipped, posture.CommandEffects)
		coordinator := profileByID(t, profiles, prompts.CoordinatorProfileID)
		for _, surfaceID := range surfaces.SurfaceIDs() {
			t.Run(string(posture.ID)+"/"+surfaceID, func(t *testing.T) {
				row, err := surfaces.Surface(surfaceID)
				contractcheck.FailErr(t, "load surface", err)
				declared, err := toolcontract.NewCommandEffects(row.CommandEffects...)
				contractcheck.FailErr(t, "compile surface effects", err)
				admitted := coordinator.CommandEffects & declared

				vars := map[string]any{"root_count": 1}
				mergePromptBudgetSkillRoster(t, vars)
				contractcheck.FailErr(t, "merge surface vars", prompts.MergeCoordinatorSurfacePathVars(surfaceID, profiles, vars, prompts.SurfaceTurn{}))
				contractcheck.FailErr(t, "merge prompt vars", prompts.MergeCoordinatorPromptVars(surfaceID, prompts.ExecutionModePromptTransition{}, "", vars))
				offered, _ := vars["surface_offered"].([]string)
				commandOffered := slices.Contains(offered, "command")
				for name, class := range commandEffectVariables {
					want := commandOffered && allowsEffect(t, admitted, class)
					if got, ok := vars[name].(bool); !ok || got != want {
						t.Errorf("%s = %v, want %v (admitted %v)", name, vars[name], want, admitted.Classes())
					}
				}

				assertUnitRendersIff(t, engine, "http-operating-procedure", vars, vars["profile_command_network_egress"] == true)
				restores, _ := vars["profile_has_restore_version"].(bool)
				assertUnitRendersIff(t, engine, "native-edit-tool", vars, vars["profile_command_mutation"] == true || restores)
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
	for _, posture := range postures.List() {
		profiles := postureProfiles(shipped, posture.CommandEffects)
		for _, profile := range profiles {
			if profile.ID == prompts.CoordinatorProfileID {
				continue
			}
			t.Run(string(posture.ID)+"/"+profile.ID, func(t *testing.T) {
				vars := map[string]any{}
				contractcheck.FailErr(t, "merge profile vars", prompts.MergeAgentToolSurfaceVars(profile.ID, nil, nil, prompts.SurfaceTurn{}, vars, profiles))
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

// The checklist procedure follows the one tool that stores the checklist.
func TestProgressProcedureFollowsUpdateProgress(t *testing.T) {
	catalog, err := prompts.UnitCatalogForEngine(bundledPromptEngine())
	contractcheck.FailErr(t, "unit catalog", err)
	unit, ok := catalog.Unit("coordinator-progress-procedure")
	if !ok {
		t.Fatal("coordinator-progress-procedure is not a shipped unit")
	}
	if !slices.Equal(unit.Attaches, []string{"update_progress"}) {
		t.Fatalf("attaches = %v", unit.Attaches)
	}
}

func assertUnitRendersIff(t *testing.T, engine *prompts.FileTemplateEngine, stem string, vars map[string]any, want bool) {
	t.Helper()
	body, err := engine.RenderUnit(t.Context(), "units/"+stem, vars)
	contractcheck.FailErr(t, "render "+stem, err)
	if got := strings.TrimSpace(body) != ""; got != want {
		t.Errorf("unit %s rendered = %v, want %v", stem, got, want)
	}
}
