package contract

import (
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorSurfaceInvariants_promptCallableToolsOnImplementSurface(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	profile := loadCoordinatorProfileTools(t)

	cases := []struct {
		name           string
		surfaceID      string
		corpus         string
		extraAllowlist map[string]string
	}{
		{
			name:      "routing",
			surfaceID: surfaceImplementRouting,
			corpus: strings.Join([]string{
				fileText(t, catalogfixture.FindStockAgentPrompt(t, "coordinator-mode-implement-routing.md")),
				renderCoordinatorTripartiteForRunContext(t, root,
					api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild},
					surface.HostLoopWakeSentinel, nil, surfaceImplementRouting),
			}, "\n"),
			extraAllowlist: routingPromptToolsAllowedOffWire,
		},
		{
			name:      "dispatch",
			surfaceID: surfaceImplementDispatch,
			corpus: strings.Join([]string{
				fileText(t, catalogfixture.FindStockAgentPrompt(t, "coordinator-mode-implement-dispatch.md")),
				renderCoordinatorTripartiteForRunContext(t, root,
					api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild},
					surface.HostLoopWakeSentinel, WorkerCompletionHistory(orchestration.ProfileImplementer), surfaceImplementDispatch),
			}, "\n"),
			extraAllowlist: map[string]string{
				"read":            "synthesis surface only — dispatch is write roster",
				"grep":            "synthesis surface only",
				"survey_repo":     "synthesis/investigate surfaces own catalog bundle survey",
				"find":            "synthesis surface only",
				"pack_board":      "dispatch turn forbids pack_board in mode copy",
				"list_dir":        "routing/synthesis/investigate survey; dispatch wire stays task-only",
				"scan_pack":       "synthesis/core scan policy",
				"web_search":      "coordinator-core invariant 6; dispatch wire stays task-only",
				"fetch_url":       "coordinator-core invariant 6; dispatch wire stays task-only",
				"preview_overlay": "worker-chain baseline describes integration; overlay-promote surface provides preview",
				"promote_overlay": "worker-chain baseline describes integration; overlay-promote surface provides promote",
			},
		},
		{
			name:      "synthesis",
			surfaceID: surfaceImplementSynthesis,
			corpus: renderCoordinatorTripartiteForRunContext(t, root,
				api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild},
				"Summarize findings",
				[]api.Message{
					{Role: api.MessageRoleUser, Content: "First question"},
					{Role: api.MessageRoleAssistant, Content: "First answer"},
				}, "implement_synthesis"),
			extraAllowlist: map[string]string{
				"task":                  "wrapup forbidden table — not on wire",
				"pack_board":            "wrapup forbidden table — not on wire",
				"wait":                  "wrapup forbidden table — not on wire",
				"worker_cancel":         "wrapup forbidden table — not on wire",
				"answer_decision":       "wrapup forbidden table — not on wire",
				"extend_worker_budget":  "wrapup forbidden table — not on wire",
				"decline_worker_budget": "wrapup forbidden table — not on wire",
				"promote_overlay":       "wrapup forbidden table — not on wire",
				"reject_overlay":        "wrapup forbidden table — not on wire",
				"preview_overlay":       "wrapup forbidden table — not on wire",
			},
		},
		{
			name:      "investigate",
			surfaceID: "implement_investigate",
			corpus: renderCoordinatorTripartiteForRunContext(t, root,
				api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild},
				"Fix the bug in internal/session",
				nil, "implement_investigate"),
			extraAllowlist: map[string]string{
				"scan_pack":       "core scan policy; investigate uses native scan_* drill-down",
				"promote_overlay": "investigate pacing describes write-leg integration; overlay-promote surface provides promote",
			},
		},
		{
			name:      "overlay_promote",
			surfaceID: surfaceImplementOverlayPromote,
			corpus: strings.Join([]string{
				fileText(t, catalogfixture.FindStockAgentPrompt(t, "coordinator-mode-implement-overlay-promote.md")),
				renderCoordinatorTripartiteForRunContext(t, root,
					api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild},
					surface.HostLoopWakeSentinel,
					WorkerCompletionHistory(orchestration.ProfileImplementer),
					surfaceImplementOverlayPromote,
					surface.ImplementSessionState{PendingOverlayIDs: []string{"job-overlay-1"}, WorkersInFlight: 1},
				),
			}, "\n"),
			extraAllowlist: orchestrateSharedPromptToolsAllowedOffWire,
		},
		{
			name:      "park",
			surfaceID: surfaceImplementPark,
			corpus: renderCoordinatorTripartiteForRunContext(t, root,
				api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild},
				surface.HostLoopWakeSentinel,
				WorkerCompletionHistory(orchestration.ProfileImplementer),
				surfaceImplementPark,
				surface.ImplementSessionState{WorkersInFlight: 2},
			),
			extraAllowlist: mergeToolAllowlists(
				orchestratePromptToolsAllowedOffWire,
				orchestrateSharedPromptToolsAllowedOffWire,
				map[string]string{
					"find":        "coordinator-core / surface-build Lane S copy; park wire is lifecycle-only",
					"survey_repo": "survey-first-pass partial; park wire is lifecycle-only",
				},
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: tc.surfaceID}, 1)
			contractcheck.FailErr(t, "compile tool plan", err)
			wireTools := plan.AddressableNames()
			var violations []string
			for tool, enabled := range profile.Tools {
				if !enabled || !mentionsToolName(tc.corpus, tool) {
					continue
				}
				if _, ok := implementPromptToolsNotOnWire[tool]; ok {
					continue
				}
				if tc.extraAllowlist != nil {
					if _, ok := tc.extraAllowlist[tool]; ok {
						continue
					}
				}
				if !toolOnImplementSurface(wireTools, tool) {
					violations = append(violations, tool+" (mentioned in implement "+tc.name+" prompt but absent from "+tc.surfaceID+")")
				}
			}
			sort.Strings(violations)
			if len(violations) > 0 {
				t.Fatalf("prompt ⊆ schema violations:\n  %s\nAdd tool to %s in coordinator-surfaces.yaml or document in implementPromptToolsNotOnWire / routingPromptToolsAllowedOffWire.",
					strings.Join(violations, "\n  "), tc.surfaceID)
			}
		})
	}
}

func TestCoordinatorSurfaceInvariants_specialistAgentsNotOnWire(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	wire := append(append(append([]string(nil), surfaces[surfaceImplementRouting]...), surfaces[surfaceImplementDispatch]...), surfaces[surfaceImplementSynthesis]...)
	for _, agentID := range specialistSpawnAgents {
		if contractcheck.ContainsString(wire, agentID) {
			t.Fatalf("agent %q must be spawned via task(), not listed as coordinator wire tool", agentID)
		}
	}
}

func TestCoordinatorSurfaceInvariants_specialistAgentsSpawnable(t *testing.T) {
	t.Parallel()
	allowed := spawn.AmbientAllowedAgents()
	for _, agentID := range specialistSpawnAgents {
		if !contractcheck.ContainsString(allowed, agentID) {
			t.Fatalf("specialist agent %q must be in AmbientAllowedAgents", agentID)
		}
	}
}

func TestCoordinatorSurfaceInvariants_workersExpandCapabilityBeyondCoordinatorWire(t *testing.T) {
	t.Parallel()
	coord := loadCoordinatorProfileTools(t)
	surfaces := loadImplementSurfaces(t)
	synthesis := surfaces[surfaceImplementSynthesis]

	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	byID := map[string]sandbox.ToolProfile{}
	for _, p := range profiles {
		byID[p.ID] = p
	}
	impl, ok := byID["implement"]
	if !ok {
		t.Fatal("missing implement profile")
	}
	explore, ok := byID["explore_readonly"]
	if !ok {
		t.Fatal("missing explore_readonly profile")
	}
	if !impl.Tools["write"] {
		t.Fatal("implementer profile must grant write")
	}
	if contractcheck.ContainsString(synthesis, "write") {
		t.Fatal("implement_synthesis wire must not expose write — delegate to implementer via task()")
	}
	if explore.Tools["command"] {
		t.Fatal("explore_readonly profile must not grant command — native survey only")
	}
	if !explore.Tools["git_diff"] {
		t.Fatal("explore_readonly profile must grant git_diff for VCS survey")
	}
	// Coordinator grants command; the turn surface gates it.
	if !coord.Tools["command"] {
		t.Fatal("coordinator profile must grant command as its ceiling (surface-gated per turn)")
	}
	if contractcheck.ContainsString(surfaces[surfaceImplementDispatch], "command") {
		t.Fatal("implement_dispatch must not expose command — orchestration delegates execution to workers")
	}
}
