package promptrender_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

type outputReaderRoute string

const (
	routeCallable    outputReaderRoute = "callable"
	routeLoadable    outputReaderRoute = "loadable"
	routeUnavailable outputReaderRoute = "unavailable"
)

type jobOccKind string

const (
	occRunning    jobOccKind = "running"
	occExitedOnly jobOccKind = "exited_only"
	occNoJob      jobOccKind = "no_job"
)

type jobWaitAuthorityRep struct {
	label         string
	isWorker      bool
	coordState    hoststate.CoordinatorState
	workerState   hoststate.WorkerState
	auth          hoststate.TurnAuthority
	overrideMetas []tools.ToolMeta
	route         outputReaderRoute
	waitAdmitted  bool
}

func classifyOutputReaderRoute(auth hoststate.TurnAuthority) outputReaderRoute {
	if slices.Contains(auth.Offered, "command_output") {
		return routeCallable
	}
	if slices.Contains(auth.Reader.Requestable(), "command_output") && slices.Contains(auth.Offered, "request_tools") {
		return routeLoadable
	}
	return routeUnavailable
}

// jobClock is the fixed render time for job blocks the test renders directly.
var jobClock = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

func renderJobGuidanceForRep(t *testing.T, s *promptrender.Stock, rep jobWaitAuthorityRep, jobs []bgprocess.JobSnapshot) []string {
	t.Helper()
	if rep.isWorker {
		block := inject.RenderCommandJobsBlock(
			t.Context(), prompts.NewInjectRenderer(s.Engine()),
			rep.auth.Reader, rep.auth.Schemas["wait"],
			"sess-worker-job", jobs, nil, jobClock, "worker:"+rep.workerState.AgentID,
		)
		if strings.TrimSpace(block) == "" {
			return nil
		}
		return []string{block}
	}
	wsDir := ""
	if rep.coordState.RootCount > 0 {
		wsDir = t.TempDir()
	}
	cs := rep.coordState
	overlay := cs.Overlay
	if overlay.Catalog == nil {
		overlays, err := hoststate.DiscoverPackOverlays(t.Context())
		if err != nil {
			t.Fatalf("DiscoverPackOverlays: %v", err)
		}
		overlay = overlays[0]
	}
	exec, narrowed, err := hoststate.ExecutorForOverlay(
		overlay, cs.Posture, false, cs.WebSearch, cs.CommandExternalHidden, cs.MCPState,
	)
	if err != nil {
		t.Fatalf("ExecutorForOverlay(%s): %v", rep.label, err)
	}
	effCat := s.Catalog
	effSchemas := s.Schemas
	if overlay.Catalog != nil {
		effCat = overlay.Catalog
	}
	if overlay.Schemas != nil {
		effSchemas = overlay.Schemas
	}
	view := &catalogview.View{
		Catalog:      effCat,
		ToolProfiles: narrowed,
		ToolSchemas:  effSchemas,
	}
	sess := &api.Session{
		ID: "sess-coord-3way", ProjectID: "proj-3way", WorkspacePath: wsDir,
		AgentType: prompts.CoordinatorProfileID,
		Posture:   cs.Posture, Mode: cs.SessionMode,
	}
	history := []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "Check current progress."}}
	runCtx := hoststate.NormalizeCoordinatorRunContext(cs, sess, history)
	pe := s.Engine()
	eng := &assembly.AssemblyEngine{}
	eng.SetDeps(assembly.AssemblyDeps{
		Prompts: pe,
		Injects: prompts.NewInjectRenderer(pe),
		TurnReader: func(ctx context.Context, _ *api.Session, _ []api.Message, _ inject.CoordinatorTurnFrame) (prompts.Reader, error) {
			if len(jobs) == 0 {
				return rep.auth.Reader, nil
			}
			promoted, err := hoststate.FreezeTurnAuthorityWithResources(ctx, cs, toolcontract.ResourcePresence{CommandJobs: true})
			return promoted.Reader, err
		},
		SessionView:      func(context.Context, *api.Session) *catalogview.View { return view },
		Selection:        staticSelection{loaded: cs.Loaded},
		Limits:           func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: staticCoordFrame{ctx: runCtx},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return cs.ImplementState
		},
		EffectivePromptSurface: func(context.Context, *api.Session) prompts.AgentPromptSurface {
			return prompts.AgentPromptSurface{
				Posture:               cs.ApprovalPosture,
				CommandExternalHidden: cs.CommandExternalHidden,
				SessionScratchDir:     cs.SessionScratchDir,
				SharedTempLocations:   cs.SharedTempLocations,
			}
		},
		PromptToolLister: func(ctx context.Context, _ *api.Session, profileID string) ([]tools.ToolMeta, error) {
			if len(rep.overrideMetas) > 0 {
				return slices.Clone(rep.overrideMetas), nil
			}
			return exec.Metadata.List(ctx, platform.ToolFilter{
				ProfileID:        profileID,
				ProjectRootCount: cs.RootCount,
			})
		},
		WebSearchEnabled: func() bool { return cs.WebSearch },
		WorkspaceRoots: func(_ context.Context, s2 *api.Session) ([]map[string]any, int, string, int) {
			if s2 == nil || cs.RootCount == 0 || strings.TrimSpace(s2.WorkspacePath) == "" {
				return nil, 0, "", 0
			}
			return ensureWorkspaceTopologyDirs(s2.WorkspacePath, cs.RootCount, cs.HasGit, cs.HasAnyGitRoot)
		},
		CommandJobs: func(string) []bgprocess.JobSnapshot { return slices.Clone(jobs) },
	})
	msgs, err := eng.BuildCompletionMessages(t.Context(), sess, history, nil)
	if err != nil {
		t.Fatalf("BuildCompletionMessages(%s): %v", rep.label, err)
	}
	return newlyDeliveredHostInstructions(msgs, "")
}

// verifyJobGuidanceActions derives the expected job actions from the job
// snapshots and the recipient's offered, requestable and projected schemas, and
// compares them with the calls parsed from the delivered host blocks.
func verifyJobGuidanceActions(
	cat *toolvocab.Catalog,
	hostBlocks []string,
	jobs []bgprocess.JobSnapshot,
	auth hoststate.TurnAuthority,
) error {
	hasRunning := false
	for _, j := range jobs {
		if !j.Exited {
			hasRunning = true
		}
	}
	route := classifyOutputReaderRoute(auth)
	waitAdmitted := slices.Contains(auth.Offered, "wait") && inject.WaitSchemaAllowsProcessDone(auth.Schemas["wait"])

	joined := strings.Join(hostBlocks, "\n")
	directCalls := parsedCallsForTool(cat, hostBlocks, "command_output")
	hasOutputLoader := slices.ContainsFunc(parsedCallsForTool(cat, hostBlocks, "request_tools"), func(ex toolvocabtest.CallExample) bool {
		return fmt.Sprint(ex.Args["need"]) == "command_output"
	})
	hasWaitProcessDone := slices.ContainsFunc(parsedCallsForTool(cat, hostBlocks, "wait"), func(ex toolvocabtest.CallExample) bool {
		return strings.Contains(ex.Raw, "process_done")
	})

	if len(jobs) == 0 {
		if len(directCalls) > 0 || hasOutputLoader || hasWaitProcessDone {
			return fmt.Errorf("no job, yet job actions delivered (direct=%d, loader=%t, wait=%t)", len(directCalls), hasOutputLoader, hasWaitProcessDone)
		}
		return nil
	}
	for _, j := range jobs {
		if !strings.Contains(joined, j.Handle) {
			return fmt.Errorf("job handle %s not delivered", j.Handle)
		}
	}
	if hasRunning {
		if (len(directCalls) > 0) != (route == routeCallable) {
			return fmt.Errorf("running job command_output call=%t, route=%s", len(directCalls) > 0, route)
		}
		if hasOutputLoader != (route == routeLoadable) {
			return fmt.Errorf("running job request_tools(need=\"command_output\")=%t, route=%s", hasOutputLoader, route)
		}
		if hasWaitProcessDone != waitAdmitted {
			return fmt.Errorf("running job wait(process_done)=%t, wait admitted=%t", hasWaitProcessDone, waitAdmitted)
		}
		return nil
	}
	if hasWaitProcessDone {
		return fmt.Errorf("exited job delivered wait(process_done)")
	}
	if len(directCalls) > 0 {
		return fmt.Errorf("exited job delivered a live command_output call")
	}
	if hasOutputLoader != (route == routeLoadable) {
		return fmt.Errorf("exited job request_tools(need=\"command_output\")=%t, route=%s", hasOutputLoader, route)
	}
	return nil
}

// Every job occurrence, on one representative recipient for each realizable
// output-reader route and wait admission, delivers exactly the job actions the
// recipient can take.
func TestCommandJobGuidanceFollowsOutputRouteAndWaitAdmission(t *testing.T) {
	s := mustStock(t)
	coordStates, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}
	workerStates, err := hoststate.WorkerStates(t.Context())
	if err != nil {
		t.Fatalf("WorkerStates: %v", err)
	}

	reps := map[string]jobWaitAuthorityRep{}
	for _, cs := range coordStates {
		if cs.Excluded || cs.PackOverlayID != "stock" {
			continue
		}
		auth, fErr := hoststate.FreezeTurnAuthorityWithResources(t.Context(), cs, toolcontract.ResourcePresence{CommandJobs: true})
		if fErr != nil {
			t.Fatalf("FreezeTurnAuthorityWithResources(%s): %v", cs.ID, fErr)
		}
		rt := classifyOutputReaderRoute(auth)
		wa := slices.Contains(auth.Offered, "wait") && inject.WaitSchemaAllowsProcessDone(auth.Schemas["wait"])
		key := fmt.Sprintf("route=%s|wait_process_done=%t", rt, wa)
		if _, exists := reps[key]; !exists {
			reps[key] = jobWaitAuthorityRep{
				label: cs.ID, coordState: cs, auth: auth, route: rt, waitAdmitted: wa,
			}
		}
	}
	for _, ws := range workerStates {
		if ws.Excluded || ws.Overlay.ID != "stock" {
			continue
		}
		auth, fErr := hoststate.FreezeWorkerAuthority(t.Context(), ws)
		if fErr != nil {
			t.Fatalf("FreezeWorkerAuthority(%s): %v", ws.ID, fErr)
		}
		rt := classifyOutputReaderRoute(auth)
		wa := slices.Contains(auth.Offered, "wait") && inject.WaitSchemaAllowsProcessDone(auth.Schemas["wait"])
		key := fmt.Sprintf("route=%s|wait_process_done=%t", rt, wa)
		if _, exists := reps[key]; !exists {
			reps[key] = jobWaitAuthorityRep{
				label: ws.ID, isWorker: true, workerState: ws, auth: auth, route: rt, waitAdmitted: wa,
			}
		}
		if ws.AgentID == "implementer" && ws.Loading == hoststate.WorkerLoadingStickyOnly {
			if _, hasLoadableWait := reps["route=loadable|wait_process_done=true"]; !hasLoadableWait {
				wsWaitLoaded := ws
				wsWaitLoaded.ID = ws.ID + "+loaded=wait"
				wsWaitLoaded.Loaded = map[string]bool{"wait": true}
				if wAuth, wErr := hoststate.FreezeWorkerAuthority(t.Context(), wsWaitLoaded); wErr == nil {
					wRt := classifyOutputReaderRoute(wAuth)
					wWa := slices.Contains(wAuth.Offered, "wait") && inject.WaitSchemaAllowsProcessDone(wAuth.Schemas["wait"])
					if wRt == routeLoadable && wWa {
						reps["route=loadable|wait_process_done=true"] = jobWaitAuthorityRep{
							label: wsWaitLoaded.ID, isWorker: true, workerState: wsWaitLoaded, auth: wAuth, route: wRt, waitAdmitted: wWa,
						}
					}
				}
			}
		}
	}

	if len(reps) == 0 {
		t.Fatal("no stock recipient state")
	}
	for _, key := range slices.Sorted(maps.Keys(reps)) {
		rep := reps[key]
		for _, occ := range []jobOccKind{occRunning, occExitedOnly, occNoJob} {
			jobs := fixedJobSnapshots(occ, jobClock)
			blocks := renderJobGuidanceForRep(t, s, rep, jobs)
			if err := verifyJobGuidanceActions(s.Names, blocks, jobs, rep.auth); err != nil {
				t.Errorf("%s on %s (%s): %v", occ, key, rep.label, err)
			}
		}
	}

	callableRep, ok := reps["route=callable|wait_process_done=true"]
	if !ok {
		t.Fatal("no stock state offers command_output and wait(process_done)")
	}
	implementer := pickWorkerRep(t, workerStates, "implementer", api.SessionPostureBuild)
	injR := prompts.NewInjectRenderer(s.Engine())
	held := []heldcall.Running{{Handle: "held-1", Tool: "command", Elapsed: 8 * time.Second}}
	for _, rep := range []jobWaitAuthorityRep{callableRep, implementer} {
		surface := "coordinator"
		if rep.isWorker {
			surface = "worker:" + rep.workerState.AgentID
		}
		runBlock := inject.RenderCommandJobsBlock(t.Context(), injR, rep.auth.Reader, rep.auth.Schemas["wait"], "sess-stop", fixedJobSnapshots(occRunning, jobClock), nil, jobClock, surface)
		if got, want := len(parsedCallsForTool(s.Names, []string{runBlock}, "command_stop")) > 0, slices.Contains(rep.auth.Offered, "command_stop"); got != want {
			t.Errorf("%s: command_stop call=%t, offered=%t", rep.label, got, want)
		}
		heldBlock := inject.RenderCommandJobsBlock(t.Context(), injR, rep.auth.Reader, rep.auth.Schemas["wait"], "sess-held", nil, held, jobClock, surface)
		if got, want := len(parsedCallsForTool(s.Names, []string{heldBlock}, "held_result")) > 0, slices.Contains(rep.auth.Offered, "held_result"); got != want {
			t.Errorf("%s: held_result call=%t, offered=%t", rep.label, got, want)
		}
	}
}

func pickWorkerRep(t *testing.T, workerStates []hoststate.WorkerState, agentID string, posture api.SessionPosture) jobWaitAuthorityRep {
	t.Helper()
	for _, ws := range workerStates {
		if !ws.Excluded && ws.Overlay.ID == "stock" && ws.AgentID == agentID && ws.Posture == posture && ws.Loading == hoststate.WorkerLoadingAllLoaded {
			wAuth, wErr := hoststate.FreezeWorkerAuthority(t.Context(), ws)
			if wErr == nil {
				return jobWaitAuthorityRep{label: ws.ID, isWorker: true, workerState: ws, auth: wAuth}
			}
		}
	}
	t.Fatalf("no stock %s worker state in %s posture", agentID, posture)
	return jobWaitAuthorityRep{}
}
