package contract

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorSurfaceInvariants_overlayPromoteIncludesWait(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	promote := surfaces[surfaceImplementOverlayPromote]
	if !contractcheck.ContainsString(promote, "wait") {
		t.Fatalf("%s must include wait for wait(resume=true) while siblings in flight", surfaceImplementOverlayPromote)
	}
}

func TestCoordinatorSurfaceInvariants_overlayPromoteResolveWorkflow(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	promote := surfaces[surfaceImplementOverlayPromote]
	for _, required := range []string{"promote_overlay", "reject_overlay", "preview_overlay", "answer_decision", "extend_worker_budget", "decline_worker_budget", "update_progress", "task", "wait", "read"} {
		if !contractcheck.ContainsString(promote, required) {
			t.Fatalf("%s must include %q", surfaceImplementOverlayPromote, required)
		}
	}
	for _, forbidden := range []string{"edit", "replace_lines", "delegate_dispatch"} {
		if contractcheck.ContainsString(promote, forbidden) {
			t.Fatalf("%s must not expose %q — integrate via promote_overlay/reject_overlay or task() for partial legs", surfaceImplementOverlayPromote, forbidden)
		}
	}
}

func TestCoordinatorSurfaceInvariants_taskIncludesWorkerLifecycle(t *testing.T) {
	t.Parallel()
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile coordinator tool plans", err)
	for id, plan := range plans {
		if !plan.Addressable("task") {
			continue
		}
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			for _, tool := range orchestrationLifecycleTools {
				if !plan.Addressable(tool) {
					t.Errorf("surface %s can dispatch workers but cannot manage their lifecycle: missing %s", id, tool)
				}
				if plan.Deferred(tool) && !plan.Immediate("request_tools") {
					t.Errorf("surface %s defers %s without offering request_tools", id, tool)
				}
			}
		})
	}
}

func TestCoordinatorSurfaceInvariants_orchestrationLifecycleOnPinnedSurfaces(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	for _, id := range orchestrationPinnedSurfaces {
		wire := surfaces[id]
		for _, tool := range orchestrationLifecycleTools {
			if !contractcheck.ContainsString(wire, tool) {
				t.Fatalf("%s must include orchestration lifecycle tool %q — host can pin this surface while coordinator-core references it", id, tool)
			}
		}
	}
}

func TestCoordinatorSurfaceInvariants_pinnedSurfacePromptMentionsLifecycleTools(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	profile := loadCoordinatorProfileTools(t)
	surfaces := loadImplementSurfaces(t)

	cases := []struct {
		name      string
		surfaceID string
		corpus    string
	}{
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
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wire := surfaces[tc.surfaceID]
			for _, tool := range orchestrationLifecyclePromptTools {
				if !coordinatorProfileGrantsTool(profile, tool) {
					t.Fatalf("coordinator profile must grant lifecycle tool %q", tool)
				}
				if !mentionsToolName(tc.corpus, tool) {
					t.Fatalf("%s prompt corpus must mention lifecycle tool %q (coordinator-core orchestrate copy)", tc.name, tool)
				}
				if !toolOnImplementSurface(wire, tool) {
					t.Fatalf("%s wire must expose lifecycle tool %q — no allowlist escape hatch", tc.surfaceID, tool)
				}
			}
		})
	}
}

func TestCoordinatorSurfaceInvariants_parkSurfaceOrchestrationLifecycle(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	park := surfaces[surfaceImplementPark]
	for _, tool := range []string{"task", "pack_board"} {
		if !contractcheck.ContainsString(park, tool) {
			t.Fatalf("implement_park must expose %q for partial dispatch repair", tool)
		}
	}
	if len(park) == 0 {
		t.Fatalf("%s missing from coordinator-surfaces.yaml", surfaceImplementPark)
	}
	for _, tool := range orchestrationLifecycleTools {
		if !contractcheck.ContainsString(park, tool) {
			t.Fatalf("%s must include %q", surfaceImplementPark, tool)
		}
	}
	for _, forbidden := range []string{"read", "grep", "promote_overlay", "delegate_dispatch"} {
		if contractcheck.ContainsString(park, forbidden) {
			t.Fatalf("%s must stay lean — unexpected tool %q", surfaceImplementPark, forbidden)
		}
	}
}

func TestCoordinatorSurfaceInvariants_synthesisNotRoutingSuperset(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	routing := surfaces[surfaceImplementRouting]
	synthesis := surfaces[surfaceImplementSynthesis]
	for _, orchestrationTool := range []string{"task", "wait", "pack_board"} {
		if contractcheck.ContainsString(synthesis, orchestrationTool) {
			t.Fatalf("implement_synthesis must not expose %q — wrapup is read-only", orchestrationTool)
		}
	}
	if !contractcheck.ContainsString(synthesis, "update_progress") {
		t.Fatal("implement_synthesis must expose update_progress for checklist reconciliation")
	}
	// Synthesis cannot contain the full routing surface.
	var routingOnly []string
	for _, routingTool := range routing {
		if !contractcheck.ContainsString(synthesis, routingTool) {
			routingOnly = append(routingOnly, routingTool)
		}
	}
	if len(routingOnly) == 0 {
		t.Fatalf("implement_synthesis (%d tools) is a superset of implement_routing (%d tools) — read-only wrapup must not contain every routing tool", len(synthesis), len(routing))
	}
}

// Closeout surfaces are catalogued.
func TestCoordinatorCloseoutSurfacesTrackYAMLSSOT(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)

	closeoutIDs := []string{tools.SurfaceImplementInvestigate, spawn.SurfaceImplementSynthesis}
	for _, id := range closeoutIDs {
		if !surface.SurfaceDeliversReport(id) {
			t.Fatalf("expected %q to be a coordinator closeout surface", id)
		}
		if _, ok := surfaces[id]; !ok {
			t.Fatalf("closeout surface %q is not defined in coordinator-surfaces.yaml — a rename must update surface.SurfaceDeliversReport", id)
		}
	}

	assertDenNamesNoCloseoutSurface(t, closeoutIDs)
}

// Den receives closeout state through loop progress.
func assertDenNamesNoCloseoutSurface(t *testing.T, closeoutIDs []string) {
	t.Helper()
	denSrc := filepath.Join(contractcheck.RepoRoot(t), "lycaon-den", "src")
	var offenders []string
	err := filepath.WalkDir(denSrc, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".ts") && !strings.HasSuffix(name, ".tsx") {
			return nil
		}
		if strings.Contains(name, ".test.") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, id := range closeoutIDs {
			if strings.Contains(string(body), `"`+id+`"`) {
				rel, _ := filepath.Rel(denSrc, path)
				offenders = append(offenders, rel+" names "+id)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk den src", err)
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("Den must not name a coordinator closeout surface — branch on CoordinatorLoopProgress.provisional_hidden instead:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
