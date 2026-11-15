package contract

import (
	"fmt"
	"maps"
	"reflect"
	"testing"

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

					// 1. Standing prompt entry point (MergeCoordinatorSurfacePathVars + capability.MergeForTurn).
					standingVars := map[string]any{}
					contractcheck.FailErr(t, "standing surface vars", prompts.MergeCoordinatorSurfacePathVars(surfaceID, profiles, standingVars, in))
					contractcheck.FailErr(t, "standing capability merge", capability.MergeForTurn(
						standingVars,
						reader,
						spawn.AmbientAllowedAgents(),
					))

					// 2. Transition inject entry point (MergeCoordinatorSurfacePathVars).
					transitionVars := map[string]any{}
					contractcheck.FailErr(t, "transition surface vars", prompts.MergeCoordinatorSurfacePathVars(surfaceID, profiles, transitionVars, in))

					// 3. Spawn inject entry point (reader.Facts() + surf.MergeSpawnInjectVars()).
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
					spawnVars := reader.Facts()
					maps.Copy(spawnVars, surf.MergeSpawnInjectVars())

					// 4. Tool-procedures inject entry point (reader.Facts()).
					procedureVars := reader.Facts()

					// 5. Kick render entry point (MergeCoordinatorKickVarsForTools).
					kickVars := map[string]any{}
					prompts.MergeCoordinatorKickVarsForTools(kickVars, reader)

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
