package contract

import (
	"fmt"
	"maps"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/spawn"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every coordinator render entry point receives identical values for every
// reader-fact key on a given (surface, posture, root count) turn.
func TestCoordinatorEntryPointsAgreeOnReaderFacts(t *testing.T) {
	surfaces, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load surfaces", err)
	shipped, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	postures, err := sessionprofiles.LoadPostureRegistry()
	contractcheck.FailErr(t, "load postures", err)

	for _, posture := range postures.List() {
		profiles := postureProfiles(shipped, posture.CommandEffects)
		for _, rootCount := range []int{0, 1, 2} {
			for _, surfaceID := range surfaces.SurfaceIDs() {
				t.Run(fmt.Sprintf("%s/roots=%d/%s", posture.ID, rootCount, surfaceID), func(t *testing.T) {
					plan, err := prompts.CompileSurfacePlan(surfaceID, rootCount)
					contractcheck.FailErr(t, "compile surface plan", err)
					in := prompts.ReaderInput{
						ProfileID: prompts.CoordinatorProfileID,
						SurfaceID: surfaceID,
						Plan:      plan,
						Profiles:  profiles,
						RootCount: rootCount,
						WebSearch: true,
					}
					reader, err := prompts.NewReader(in)
					contractcheck.FailErr(t, "new reader", err)
					want := reader.Facts()

					// 1. Standing prompt entry point (capability.MergeForTurn + prompts.Vars).
					standingOcc := map[string]any{}
					contractcheck.FailErr(t, "standing capability merge", capability.MergeForTurn(
						standingOcc,
						reader,
						spawn.AmbientAllowedAgents(),
					))
					standingVars, err := prompts.Vars(reader, standingOcc)
					contractcheck.FailErr(t, "standing vars", err)

					// 2. Transition inject entry point (prompts.Vars).
					transitionVars, err := prompts.Vars(reader, nil)
					contractcheck.FailErr(t, "transition vars", err)

					// 3. Spawn inject entry point (prompts.Vars(reader, surf.MergeSpawnInjectVars())).
					webSearchEnabled := reader.WebSearch()
					surf, err := capability.Resolve(capability.ResolveInput{
						Profile:          surface.TurnProfile{SurfaceID: surfaceID},
						RootCount:        rootCount,
						SpawnAllowlist:   spawn.AmbientAllowedAgents(),
						MaxInFlight:      spawn.MaxInFlightTaskWorkers,
						WebSearchEnabled: &webSearchEnabled,
						ToolProfiles:     profiles,
					})
					contractcheck.FailErr(t, "resolve capability surface", err)
					spawnVars, err := prompts.Vars(reader, surf.MergeSpawnInjectVars())
					contractcheck.FailErr(t, "spawn vars", err)

					// 4. Tool-procedures inject entry point (prompts.Vars).
					procedureVars, err := prompts.Vars(reader, nil)
					contractcheck.FailErr(t, "procedure vars", err)

					// 5. Kick render entry point (prompts.Vars).
					kickVars, err := prompts.Vars(reader, nil)
					contractcheck.FailErr(t, "kick vars", err)

					entryPoints := map[string]map[string]any{
						"standing":        standingVars,
						"transition":      transitionVars,
						"spawn_inject":    spawnVars,
						"tool_procedures": procedureVars,
						"kick":            kickVars,
					}
					for key, wantVal := range want {
						for epName, epVars := range entryPoints {
							gotVal, ok := epVars[key]
							if !ok {
								t.Errorf("%s missing reader fact %q", epName, key)
								continue
							}
							if !reflect.DeepEqual(gotVal, wantVal) {
								t.Errorf("%s[%q] = %#v, want %#v", epName, key, gotVal, wantVal)
							}
						}
					}
					_ = maps.Clone(want)
				})
			}
		}
	}
}

// Worker skill templates receive the reader's value for every reader-fact
// key on each (profile, posture, root count) leg; persona renders are held to
// the same in internal/prompts.
func TestWorkerSkillVarsCarryReaderFacts(t *testing.T) {
	eff := contractcheck.StockCatalog(t)
	agents, err := agentdef.LoadEffectiveWithCatalog(eff)
	contractcheck.FailErr(t, "load agents", err)
	shipped, err := sandbox.LoadToolProfilesWithCatalog(eff)
	contractcheck.FailErr(t, "load tool profiles", err)
	postures, err := sessionprofiles.LoadPostureRegistry()
	contractcheck.FailErr(t, "load postures", err)
	for _, posture := range postures.List() {
		profiles := postureProfiles(shipped, posture.CommandEffects)
		for _, rootCount := range []int{0, 1, 2} {
			for _, agent := range agents {
				if agent.ID == "coordinator" {
					continue
				}
				t.Run(fmt.Sprintf("%s/roots=%d/%s", posture.ID, rootCount, agent.ID), func(t *testing.T) {
					reader, err := prompts.NewReader(prompts.ReaderInput{
						ProfileID: agent.ToolProfile,
						Profiles:  profiles,
						RootCount: rootCount,
						HasGit:    rootCount > 0,
						WebSearch: true,
					})
					contractcheck.FailErr(t, "new worker reader", err)
					want := reader.Facts()

					entryPoints := map[string]map[string]any{
						"skill": prompts.SkillVars(reader),
					}
					for key, wantVal := range want {
						for epName, epVars := range entryPoints {
							gotVal, ok := epVars[key]
							if !ok {
								t.Errorf("%s missing reader fact %q", epName, key)
								continue
							}
							if !reflect.DeepEqual(gotVal, wantVal) {
								t.Errorf("%s[%q] = %#v, want %#v", epName, key, gotVal, wantVal)
							}
						}
					}
				})
			}
		}
	}
}
