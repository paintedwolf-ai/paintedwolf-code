package assembly

import (
	"context"
	"github.com/lycaon/lycaon/internal/testutil/exposure"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/prompttest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/pkg/api"
)

type advancingTurnFrameSource struct {
	phase    string
	revision int64
}

func (s *advancingTurnFrameSource) BuildCoordinatorTurnFrame(_ context.Context, _ string, _ *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{
		WorkflowRevision: s.revision,
		RunContext: api.CoordinatorRunContext{
			WorkflowID: "atomic", WorkflowVersion: "1", RunID: "run-1",
			RunStatus: "running", CurrentPhase: s.phase,
		},
		Runtime: inject.WorkflowRuntimeSnapshot{
			Phases: []inject.WorkflowPhaseRow{
				{ID: "boot", Next: "work"},
				{ID: "work", CompleteWhen: "done", Terminal: true},
			},
			PhaseExit: &inject.PhaseExitView{Kind: s.phase + "-exit"},
		},
	}, nil
}

type advancingBoard struct{}

func (advancingBoard) PrependBoardIfChanged(_ context.Context, _ *api.Session, _ prompts.Reader, run api.CoordinatorRunContext) (string, bool) {
	return "board phase=" + run.CurrentPhase, true
}
func (advancingBoard) WorkerBoard(context.Context, *api.Session, prompts.Reader) (string, bool) {
	return "", false
}
func (advancingBoard) BoardInjectHash(string) string { return "board-v1" }
func (advancingBoard) InvalidateOrientation(string)  {}

type advancingOrientRecorder struct{ source *advancingTurnFrameSource }

func (r advancingOrientRecorder) RecordBoardOrientReady(context.Context, string, string) error {
	r.source.phase = "work"
	r.source.revision++
	return nil
}

// TestDynamicInjectsFollowHistory keeps dynamic injects after history.
func TestDynamicInjectsFollowHistory(t *testing.T) {
	eng := prefixStabilityEngine(t)
	sess := &api.Session{
		ID:            "tail-injects",
		Posture:       api.SessionPostureBuild,
		AgentType:     orchestration.ProfileCoordinator,
		WorkspacePath: t.TempDir(),
	}
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "first user turn"},
		{Role: api.MessageRoleAssistant, Content: "assistant reply"},
		{Role: api.MessageRoleUser, Content: "second user turn"},
	}

	eng.BeginPromptTurn(sess.ID, "")
	msgs, err := eng.BuildCompletionMessages(context.Background(), sess, history, nil)
	if err != nil {
		t.Fatalf("BuildCompletionMessages: %v", err)
	}

	lastHistoryIdx := -1
	for i, m := range msgs {
		if m.Content == "second user turn" {
			lastHistoryIdx = i
		}
	}
	if lastHistoryIdx < 0 {
		t.Fatal("history tail not found in assembled messages")
	}

	for i, m := range msgs[:lastHistoryIdx] {
		if isVolatilePromptBlock(m.Content) {
			t.Fatalf("dynamic inject at index %d precedes history tail at %d:\n%s", i, lastHistoryIdx, m.Content[:120])
		}
		exposure.Absent(t, m.Content, inject.ActiveWorkflowInjectSentinel)
	}

	foundVolatileAfter := false
	for _, m := range msgs[lastHistoryIdx+1:] {
		if isVolatilePromptBlock(m.Content) {
			foundVolatileAfter = true
		}
	}
	if !foundVolatileAfter {
		t.Fatal("expected workflow runtime inject after history for an active-workflow session")
	}

	if breakIdx := promptCacheMarkedIndex(msgs); breakIdx != lastHistoryIdx {
		t.Fatalf("PromptCacheBreakpoint at %d, want last history message at %d", breakIdx, lastHistoryIdx)
	}
}

func TestBoardOrientationRefreshesAtomicTurnFrameBeforeRender(t *testing.T) {
	root := prefixStabilityTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	source := &advancingTurnFrameSource{phase: "boot", revision: 1}
	eng := &AssemblyEngine{}
	eng.SetDeps(AssemblyDeps{
		Prompts: pe, Injects: prompts.NewInjectRenderer(pe),
		Limits:           func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: source,
		Board:            advancingBoard{},
		BoardOrientReady: advancingOrientRecorder{source: source},
		PromptToolLister: prompttest.CoordinatorTools,
	})
	sess := &api.Session{ID: "atomic-frame", AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir()}
	eng.BeginPromptTurn(sess.ID, "")
	msgs, err := eng.BuildCompletionMessages(context.Background(), sess, nil, nil)
	testutil.FailErr(t, "build completion messages", err)
	joined := ""
	for _, msg := range msgs {
		joined += msg.Content + "\n"
	}
	if strings.Contains(joined, "phase 1/2 — `boot`") || strings.Contains(joined, "board phase=boot") {
		t.Fatalf("prompt retained stale boot revision:\n%s", joined)
	}
	if !strings.Contains(joined, "phase 2/2 — `work`") || !strings.Contains(joined, "board phase=work") {
		t.Fatalf("prompt missing refreshed work revision:\n%s", joined)
	}
}

type childStartTurnFrameSource struct {
	runID, phase string
}

func (s *childStartTurnFrameSource) BuildCoordinatorTurnFrame(_ context.Context, _ string, _ *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{
		WorkflowID: "atomic", WorkflowVersion: "1", RunID: s.runID,
		RunStatus: "running", CurrentPhase: s.phase,
	}}, nil
}

// childStartOrientRecorder starts a child run while the parent's inject is recorded.
type childStartOrientRecorder struct {
	source   *childStartTurnFrameSource
	recorded []string
}

func (r *childStartOrientRecorder) RecordBoardOrientReady(context.Context, string, string) error {
	r.recorded = append(r.recorded, r.source.runID+"/"+r.source.phase)
	switch r.source.runID {
	case "parent":
		r.source.runID, r.source.phase = "child", "boot"
	case "child":
		r.source.phase = "work"
	}
	return nil
}

func TestBoardOrientationRecordsInjectForRefreshedRun(t *testing.T) {
	root := prefixStabilityTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	source := &childStartTurnFrameSource{runID: "parent", phase: "execute"}
	recorder := &childStartOrientRecorder{source: source}
	eng := &AssemblyEngine{}
	eng.SetDeps(AssemblyDeps{
		Prompts: pe, Injects: prompts.NewInjectRenderer(pe),
		Limits:           func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: source,
		Board:            advancingBoard{},
		BoardOrientReady: recorder,
		PromptToolLister: prompttest.CoordinatorTools,
	})
	sess := &api.Session{ID: "child-start-frame", AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir()}
	eng.BeginPromptTurn(sess.ID, "")
	msgs, err := eng.BuildCompletionMessages(context.Background(), sess, nil, nil)
	testutil.FailErr(t, "build completion messages", err)
	if len(recorder.recorded) < 2 || recorder.recorded[1] != "child/boot" {
		t.Fatalf("recorded orientations = %v, want the child boot inject recorded", recorder.recorded)
	}
	joined := ""
	for _, msg := range msgs {
		joined += msg.Content + "\n"
	}
	if !strings.Contains(joined, "board phase=work") {
		t.Fatalf("prompt missing board for the advanced child phase:\n%s", joined)
	}
}

func TestWorkerPackBoardFollowsHistory(t *testing.T) {
	root := prefixStabilityTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	inj := prompts.NewInjectRenderer(pe)
	boardEng := NewBoardEngine(placementStubBoardBuilder{hash: "worker-board"}, nil, nil)
	boardEng.SetInjectRenderer(inj)
	eng := &AssemblyEngine{}
	eng.SetDeps(AssemblyDeps{
		Prompts: pe,
		Injects: inj,
		Limits:  func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		Board:   boardEng,
	})
	sess := &api.Session{
		ID: "worker-board-tail", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
		Posture: api.SessionPostureBuild, AgentType: "implementer", WorkspacePath: t.TempDir(),
	}
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "first user turn"},
		{Role: api.MessageRoleAssistant, Content: "assistant reply"},
		{Role: api.MessageRoleUser, Content: "second user turn"},
	}
	eng.BeginPromptTurn(sess.ID, "")
	msgs, err := eng.BuildCompletionMessages(context.Background(), sess, history, nil)
	testutil.FailErr(t, "build completion messages", err)

	lastHistoryIdx := -1
	boardIdx := -1
	for i, m := range msgs {
		if m.Content == "second user turn" {
			lastHistoryIdx = i
		}
		if strings.Contains(m.Content, packboard.PackBoardSentinel) ||
			strings.Contains(m.Content, inject.BoardOrientationInjectSentinel) {
			if boardIdx < 0 {
				boardIdx = i
			}
		}
	}
	if lastHistoryIdx < 0 {
		t.Fatal("history tail not found in assembled messages")
	}
	if boardIdx < 0 {
		t.Fatal("expected worker pack board in assembled messages")
	}
	if boardIdx <= lastHistoryIdx {
		t.Fatalf("worker pack board at %d must follow history tail at %d", boardIdx, lastHistoryIdx)
	}
	if breakIdx := promptCacheMarkedIndex(msgs); breakIdx != lastHistoryIdx {
		t.Fatalf("PromptCacheBreakpoint at %d, want last history message at %d", breakIdx, lastHistoryIdx)
	}
}

func TestReportContractSurvivesRepeatedRequestsAndHistoryReplacement(t *testing.T) {
	eng := prefixStabilityEngine(t)
	sess := &api.Session{ID: "report-context", AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir()}
	frame := inject.CoordinatorTurnFrame{
		RunContext: api.CoordinatorRunContext{WorkflowID: "security-survey", RunID: "run-1", CurrentPhase: "report"},
		Runtime: inject.WorkflowRuntimeSnapshot{
			ReportDocumentEnabled: true,
			ReportRating:          &inject.ReportRatingView{Dimensions: []string{"reachable"}, Questions: "Does untrusted input reach the flawed code?"},
			Phases:                []inject.WorkflowPhaseRow{{ID: "report"}},
		},
	}
	eng.BeginPromptTurn(sess.ID)
	for i, history := range [][]api.Message{
		{{Role: api.MessageRoleUser, Content: "review"}},
		{{Role: api.MessageRoleUser, Content: "review"}, {Role: api.MessageRoleTool, Content: "progress settled"}},
		{{Role: api.MessageRoleSystem, Content: "compacted history"}},
	} {
		eng.Cache().LoadTurn(sess.ID).Iteration = i
		msgs, err := eng.BuildCompletionMessages(t.Context(), sess, history, &frame)
		testutil.FailErr(t, "assemble report request", err)
		if n := countCoordinatorRunContextBlocks(msgs); n != 1 {
			t.Fatalf("request %d has %d workflow blocks", i, n)
		}
		found := false
		for _, msg := range msgs {
			if strings.Contains(msg.Content, inject.ActiveWorkflowInjectSentinel) {
				found = strings.Contains(msg.Content, "### Report document") && strings.Contains(msg.Content, `"disposition"`) &&
					strings.Contains(msg.Content, "Does untrusted input reach the flawed code?")
			}
		}
		if !found {
			t.Fatalf("request %d lost its report contract", i)
		}
	}
}

func configureCoordinatorTestProfiles(base []sandbox.ToolProfile, waitConds []string, denyJobControl bool) []sandbox.ToolProfile {
	profiles := make([]sandbox.ToolProfile, len(base))
	copy(profiles, base)
	for i := range profiles {
		if profiles[i].ID != prompts.CoordinatorProfileID {
			continue
		}
		profiles[i].WaitConditions = slices.Clone(waitConds)
		if denyJobControl {
			clonedTools := make(map[string]bool, len(profiles[i].Tools))
			for k, v := range profiles[i].Tools {
				clonedTools[k] = v
			}
			delete(clonedTools, "command_output")
			delete(clonedTools, "command_stop")
			profiles[i].Tools = clonedTools
		}
	}
	return profiles
}

func assertCommandJobsInjectMatchesTarget(
	t *testing.T,
	msgs []api.Message,
	targetOffered []string,
	targetWaitSchema map[string]any,
	wantPromoted bool,
) {
	t.Helper()
	var liveBlock string
	for _, m := range msgs {
		if strings.Contains(m.Content, "## Live handles") {
			liveBlock = m.Content
			break
		}
	}
	if liveBlock == "" {
		t.Fatal("expected ## Live handles tail inject in assembled completion messages")
	}
	if strings.Contains(liveBlock, "command_output(handle=") != slices.Contains(targetOffered, "command_output") ||
		strings.Contains(liveBlock, "command_output(handle=") != wantPromoted {
		t.Fatalf("rendered command_output mismatch vs targetOffered=%v:\n%s", targetOffered, liveBlock)
	}
	if strings.Contains(liveBlock, "command_stop(handle=") != slices.Contains(targetOffered, "command_stop") {
		t.Fatalf("rendered command_stop mismatch vs targetOffered=%v:\n%s", targetOffered, liveBlock)
	}
	wantWaitLine := slices.Contains(targetOffered, "wait") && inject.WaitSchemaAllowsProcessDone(targetWaitSchema)
	if strings.Contains(liveBlock, `"kind":"process_done"`) != wantWaitLine {
		t.Fatalf("rendered process_done wait mismatch (want %v):\n%s", wantWaitLine, liveBlock)
	}
}

func TestCommandJobsInjectMatchesProjectedTurnRequestAcrossAxes(t *testing.T) {
	root := prefixStabilityTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	baseProfiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "CatalogForConsumers", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "LoadEffectiveToolSchemas", err)

	nonGitDir := t.TempDir()
	now := time.Now().UTC()
	liveJobs := []bgprocess.JobSnapshot{{
		Handle:     "job-live-1",
		Mode:       bgprocess.JobModeBackground,
		OriginTool: "command",
		StartedAt:  now.Add(-5 * time.Second),
	}}

	for _, tc := range []struct {
		name           string
		webSearch      bool
		waitConds      []string
		workersInFl    int
		denyJobControl bool
		wantPromoted   bool
		wantProcessW   bool
	}{
		{
			name:         "non-git root with active job promotion and process_done wait",
			webSearch:    true,
			waitConds:    []string{"process_done", "next_worker_done"},
			workersInFl:  0,
			wantPromoted: true,
			wantProcessW: true,
		},
		{
			name:         "disabled web search and narrowed worker-only wait",
			webSearch:    false,
			waitConds:    []string{"next_worker_done"},
			workersInFl:  0,
			wantPromoted: true,
			wantProcessW: false,
		},
		{
			name:         "timer-only wait schema omits process_done",
			webSearch:    true,
			waitConds:    nil,
			workersInFl:  0,
			wantPromoted: true,
			wantProcessW: false,
		},
		{
			name:           "parked surface with denied job controls omits unoffered controls",
			webSearch:      true,
			waitConds:      []string{"next_worker_done"},
			workersInFl:    1,
			denyJobControl: true,
			wantPromoted:   false,
			wantProcessW:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profiles := configureCoordinatorTestProfiles(baseProfiles, tc.waitConds, tc.denyJobControl)

			implState := func(context.Context, *api.Session) surface.ImplementSessionState {
				if tc.workersInFl > 0 {
					return surface.ImplementSessionState{WorkersInFlight: tc.workersInFl, BatchPhase: "running"}
				}
				return surface.ImplementSessionState{}
			}
			var history []api.Message
			if tc.workersInFl > 0 {
				history = []api.Message{
					{Role: api.MessageRoleUser, Content: "implement"},
					{Role: api.MessageRoleUser, Kind: api.MessageKindHostLoopWake, Content: surface.HostLoopWakeSentinel},
				}
			} else {
				history = []api.Message{{Role: api.MessageRoleUser, Content: "inspect"}}
			}

			eng := &AssemblyEngine{}
			eng.SetDeps(AssemblyDeps{
				Prompts:               pe,
				Injects:               prompts.NewInjectRenderer(pe),
				Limits:                func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
				ImplementSessionState: implState,
				WebSearchEnabled:      func() bool { return tc.webSearch },
				CommandJobs:           func(string) []bgprocess.JobSnapshot { return liveJobs },
				WorkspaceRoots: func(context.Context, *api.Session) ([]map[string]any, int, string, int) {
					return []map[string]any{{"id": "r1", "label": "root", "path": nonGitDir, "is_primary": true, "is_active": true}}, 1, nonGitDir, 0
				},
				SessionView: func(context.Context, *api.Session) *catalogview.View {
					return (&catalogview.View{ToolSchemas: schemas}).WithToolProfiles(profiles)
				},
			})

			sess := &api.Session{
				ID:            "cmd-jobs-" + strings.ReplaceAll(tc.name, " ", "-"),
				Mode:          api.SessionModeAgent,
				Posture:       api.SessionPostureBuild,
				AgentType:     orchestration.ProfileCoordinator,
				WorkspacePath: nonGitDir,
			}

			// Compute the exact target request via promptloop.ProjectTurnTools.
			pDeps := promptloop.ContextDeps{
				Policy: toolpolicy.NewEngine(toolpolicy.EngineDeps{
					ToolLister: &commandJobsToolLister{
						sess:     sess,
						profiles: profiles,
						schemas:  schemas,
					},
					ProjectRootCount: func(context.Context, *api.Session) int { return 1 },
				}),
				ImplementSessionState: implState,
				ProjectRootCount:      func(context.Context, *api.Session) int { return 1 },
				LiveResources: func(string) toolcontract.ResourcePresence {
					return toolcontract.ResourcePresence{CommandJobs: true}
				},
				ContextSurfaceDeps: promptloop.ContextSurfaceDeps{
					WebSearchEnabled: func() bool { return tc.webSearch },
				},
			}
			targetMetas, _, _, err := promptloop.ProjectTurnTools(t.Context(), pDeps, sess, history, inject.CoordinatorTurnFrame{})
			testutil.FailErr(t, "ProjectTurnTools", err)

			var targetOffered []string
			var targetWaitSchema map[string]any
			for _, meta := range targetMetas {
				targetOffered = append(targetOffered, meta.Name)
				if meta.Name == "wait" {
					targetWaitSchema = meta.ArgsSchema
				}
			}
			sort.Strings(targetOffered)

			gotReader, gotWaitSchema, err := testPromptSurface(eng).commandJobsReader(t.Context(), sess, true, inject.CoordinatorTurnFrame{}, history, nil, liveJobs, nil)
			testutil.FailErr(t, "commandJobsReader", err)
			if !reflect.DeepEqual(gotReader.Offered(), targetOffered) {
				t.Fatalf("commandJobsReader offered = %v, want target request %v", gotReader.Offered(), targetOffered)
			}
			if gotReader.Facts()["has_git"] != false || gotReader.WebSearch() != tc.webSearch {
				t.Fatalf("reader host facts mismatch: has_git=%v web_search=%v", gotReader.Facts()["has_git"], gotReader.WebSearch())
			}
			if inject.WaitSchemaAllowsProcessDone(gotWaitSchema) != inject.WaitSchemaAllowsProcessDone(targetWaitSchema) ||
				inject.WaitSchemaAllowsProcessDone(gotWaitSchema) != tc.wantProcessW {
				t.Fatalf("WaitSchemaAllowsProcessDone = %v (target %v), want %v",
					inject.WaitSchemaAllowsProcessDone(gotWaitSchema), inject.WaitSchemaAllowsProcessDone(targetWaitSchema), tc.wantProcessW)
			}

			eng.BeginPromptTurn(sess.ID, "")
			msgs, err := eng.BuildCompletionMessages(t.Context(), sess, history, nil)
			testutil.FailErr(t, "BuildCompletionMessages", err)
			assertCommandJobsInjectMatchesTarget(t, msgs, targetOffered, targetWaitSchema, tc.wantPromoted)
		})
	}
}
