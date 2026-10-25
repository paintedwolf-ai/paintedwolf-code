package assembly

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil/prompttest"
	"github.com/lycaon/lycaon/pkg/api"
)

type prefixStabilityCoordCtx struct{ ctx api.CoordinatorRunContext }

func (s prefixStabilityCoordCtx) BuildCoordinatorTurnFrame(_ context.Context, _ string, _ *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: s.ctx}, nil
}

func prefixStabilityEngine(t *testing.T) *AssemblyEngine {
	t.Helper()
	root := prefixStabilityTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	deps := AssemblyDeps{
		Prompts: pe,
		Injects: prompts.NewInjectRenderer(pe),
		Limits:  func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: prefixStabilityCoordCtx{ctx: api.CoordinatorRunContext{
			WorkflowID:    "implement",
			CurrentPhase:  "work",
			AllowedAgents: spawn.AmbientAllowedAgents(),
		}},
		PromptToolLister: prompttest.CoordinatorTools,
		WorkspaceRoots:   prefixStabilityWorkspaceRoots(),
	}
	eng := &AssemblyEngine{}
	eng.SetDeps(deps)
	return eng
}

func prefixStabilityWorkspaceRoots() WorkspaceRootsLoader {
	return func(_ context.Context, sess *api.Session) ([]map[string]any, int, string) {
		if sess == nil || strings.TrimSpace(sess.WorkspacePath) == "" {
			return nil, 0, ""
		}
		path := sess.WorkspacePath
		return []map[string]any{{
			"label":      "main",
			"path":       path,
			"is_primary": true,
		}}, 1, path
	}
}

func prefixStabilityTestRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

func TestPromptCacheStableBlockByteIdenticalAcrossTurns(t *testing.T) {
	eng := prefixStabilityEngine(t)
	sess := &api.Session{
		ID:            "prefix-stable",
		Posture:       api.SessionPostureBuild,
		AgentType:     orchestration.ProfileCoordinator,
		WorkspacePath: t.TempDir(),
	}
	ctx := context.Background()
	eng.BeginPromptTurn(sess.ID, "")
	eng.SetTurnSurfaceID(sess.ID, "implement_dispatch")
	if _, err := eng.BuildCompletionMessages(ctx, sess, nil, nil); err != nil {
		t.Fatalf("turn 1 BuildCompletionMessages: %v", err)
	}
	turn1 := eng.Cache().LoadTurn(sess.ID)
	stable1 := turn1.StablePrompt
	key1 := turn1.StablePromptKey
	if stable1 == "" || key1 == "" {
		t.Fatal("turn 1 must render and key stable prompt")
	}

	turn := eng.Cache().LoadTurn(sess.ID)
	turn.Iteration = 1
	if _, err := eng.BuildCompletionMessages(ctx, sess, nil, nil); err != nil {
		t.Fatalf("turn 2 BuildCompletionMessages: %v", err)
	}
	turn2 := eng.Cache().LoadTurn(sess.ID)
	if turn2.StablePrompt != stable1 {
		t.Fatalf("stable bytes changed turn-to-turn:\nfirst=%q\nsecond=%q", stable1, turn2.StablePrompt)
	}
	if turn2.StablePromptKey != key1 {
		t.Fatalf("stable key changed turn-to-turn: %q -> %q", key1, turn2.StablePromptKey)
	}
}

func TestPromptCacheStableKeyChangesOnStableInput(t *testing.T) {
	eng := prefixStabilityEngine(t)
	dir := t.TempDir()
	ctx := context.Background()
	sess := &api.Session{
		ID:            "prefix-key-flip",
		Posture:       api.SessionPostureBuild,
		AgentType:     orchestration.ProfileCoordinator,
		WorkspacePath: dir,
	}
	eng.BeginPromptTurn(sess.ID, "")
	eng.SetTurnSurfaceID(sess.ID, "implement_dispatch")
	if _, err := eng.BuildCompletionMessages(ctx, sess, nil, nil); err != nil {
		t.Fatalf("build: %v", err)
	}
	keyBuild := eng.Cache().LoadTurn(sess.ID).StablePromptKey

	sess.Posture = api.SessionPostureSpec
	turn := eng.Cache().LoadTurn(sess.ID)
	turn.Iteration = 0
	eng.SetTurnSurfaceID(sess.ID, "implement_dispatch")
	if _, err := eng.BuildCompletionMessages(ctx, sess, nil, nil); err != nil {
		t.Fatalf("build after posture change: %v", err)
	}
	keySpec := eng.Cache().LoadTurn(sess.ID).StablePromptKey
	if keyBuild == keySpec {
		t.Fatalf("stable key must change when posture changes: %q", keyBuild)
	}
}

func TestPromptTurnReadsWorkspaceRootsOnce(t *testing.T) {
	eng := prefixStabilityEngine(t)
	deps := eng.deps()
	reads := 0
	base := deps.WorkspaceRoots
	deps.WorkspaceRoots = func(ctx context.Context, sess *api.Session) ([]map[string]any, int, string) {
		reads++
		return base(ctx, sess)
	}
	eng.SetDeps(deps)
	sess := &api.Session{
		ID: "single-root-snapshot", Posture: api.SessionPostureBuild,
		AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir(),
	}
	eng.BeginPromptTurn(sess.ID, "")
	eng.SetTurnSurfaceID(sess.ID, "implement_dispatch")
	if _, err := eng.BuildCompletionMessages(context.Background(), sess, nil, nil); err != nil {
		t.Fatalf("BuildCompletionMessages: %v", err)
	}
	if reads != 1 {
		t.Fatalf("workspace roots loaded %d times, want one immutable turn snapshot", reads)
	}
}

func TestPromptCacheStableKeyChangesWithEffectiveCapabilitySurface(t *testing.T) {
	eng := prefixStabilityEngine(t)
	surface := prompts.AgentPromptSurface{
		HostResources: []prompts.AgentHostResourceView{{
			ID: "local-db", Label: "Local database", Status: "available", Access: "ask", Guidance: "advertise",
		}},
		Fingerprint: "ask-surface",
	}
	deps := eng.deps()
	deps.EffectivePromptSurface = func(context.Context, *api.Session) prompts.AgentPromptSurface {
		return surface
	}
	eng.SetDeps(deps)
	sess := &api.Session{
		ID: "prefix-capability-flip", Posture: api.SessionPostureBuild,
		AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir(),
	}
	ctx := context.Background()
	eng.BeginPromptTurn(sess.ID, "")
	eng.SetTurnSurfaceID(sess.ID, "implement_dispatch")
	if _, err := eng.BuildCompletionMessages(ctx, sess, nil, nil); err != nil {
		t.Fatalf("build ask surface: %v", err)
	}
	first := eng.Cache().LoadTurn(sess.ID)
	if !strings.Contains(first.StablePrompt, "`local-db`") || !strings.Contains(first.StablePrompt, "`local-db` ask") {
		t.Fatalf("capability projection missing from stable prompt: %q", first.StablePrompt)
	}
	firstKey := first.StablePromptKey

	surface.HostResources[0].Access = "deny"
	surface.Fingerprint = "deny-surface"
	first.Iteration = 0
	if _, err := eng.BuildCompletionMessages(ctx, sess, nil, nil); err != nil {
		t.Fatalf("build deny surface: %v", err)
	}
	second := eng.Cache().LoadTurn(sess.ID)
	if second.StablePromptKey == firstKey || !strings.Contains(second.StablePrompt, "`local-db` deny") {
		t.Fatalf("effective policy change left a stale stable prompt: first=%q second=%q", firstKey, second.StablePromptKey)
	}
}

func TestStandingPromptInputsKeyDerivationCoversEveryField(t *testing.T) {
	base := standingPromptInputs{
		PromptRevision: "rev-1",
		Profile: standingProfileInputs{
			SurfaceID:       "surf-1",
			SurfaceTemplate: "tmpl-1",
			ModeRefs:        []string{"mode-1"},
		},
		Vars:             map[string]any{"v1": "val1"},
		RootCount:        1,
		EffectiveAgents:  []string{"agent-1"},
		WebSearchEnabled: true,
		LoadedTools:      []string{"tool-1"},
		OmittedUnits:     []string{"unit-1"},
		ExecutionMode:    "mode-1",
		VerifyCommand:    "verify-cmd",
		WorkerToolBudget: spawn.WorkerToolBudget{Default: 10, Min: 2, Max: 50},
	}
	baseKey, err := base.cacheKey()
	if err != nil {
		t.Fatalf("base.cacheKey: %v", err)
	}

	mutateCases := []struct {
		field  string
		mutate func(*standingPromptInputs)
	}{
		{"PromptRevision", func(in *standingPromptInputs) { in.PromptRevision = "rev-2" }},
		{"Profile.SurfaceID", func(in *standingPromptInputs) { in.Profile.SurfaceID = "surf-2" }},
		{"Profile.SurfaceTemplate", func(in *standingPromptInputs) { in.Profile.SurfaceTemplate = "tmpl-2" }},
		{"Profile.ModeRefs", func(in *standingPromptInputs) { in.Profile.ModeRefs = []string{"mode-2"} }},
		{"Vars", func(in *standingPromptInputs) { in.Vars = map[string]any{"v1": "val2"} }},
		{"RootCount", func(in *standingPromptInputs) { in.RootCount = 2 }},
		{"EffectiveAgents", func(in *standingPromptInputs) { in.EffectiveAgents = []string{"agent-2"} }},
		{"WebSearchEnabled", func(in *standingPromptInputs) { in.WebSearchEnabled = false }},
		{"LoadedTools", func(in *standingPromptInputs) { in.LoadedTools = []string{"tool-2"} }},
		{"OmittedUnits", func(in *standingPromptInputs) { in.OmittedUnits = []string{"unit-2"} }},
		{"ExecutionMode", func(in *standingPromptInputs) { in.ExecutionMode = "mode-2" }},
		{"VerifyCommand", func(in *standingPromptInputs) { in.VerifyCommand = "verify-cmd-2" }},
		{"WorkerToolBudget", func(in *standingPromptInputs) { in.WorkerToolBudget.Default = 20 }},
	}

	for _, mc := range mutateCases {
		t.Run(mc.field, func(t *testing.T) {
			mutated := base
			mutated.Profile.ModeRefs = append([]string(nil), base.Profile.ModeRefs...)
			mutated.Vars = map[string]any{"v1": "val1"}
			mutated.EffectiveAgents = append([]string(nil), base.EffectiveAgents...)
			mutated.LoadedTools = append([]string(nil), base.LoadedTools...)
			mutated.OmittedUnits = append([]string(nil), base.OmittedUnits...)

			mc.mutate(&mutated)
			mutKey, err := mutated.cacheKey()
			if err != nil {
				t.Fatalf("cacheKey: %v", err)
			}
			if mutKey == baseKey {
				t.Fatalf("mutating field %s produced identical cache key %q", mc.field, mutKey)
			}
		})
	}
}

type prefixStabilityMutableCoordCtx struct {
	base          inject.CoordinatorTurnFrameSource
	evidenceGates *[]string
}

func (s prefixStabilityMutableCoordCtx) BuildCoordinatorTurnFrame(ctx context.Context, sessionID string, sess *api.Session) (inject.CoordinatorTurnFrame, error) {
	frame, err := s.base.BuildCoordinatorTurnFrame(ctx, sessionID, sess)
	if err != nil {
		return frame, err
	}
	if s.evidenceGates != nil && len(*s.evidenceGates) > 0 {
		frame.EvidenceRequirements = *s.evidenceGates
	}
	return frame, nil
}

func TestPromptCacheMidTurnGateFlipsPreservePrefixCache(t *testing.T) {
	eng := prefixStabilityEngine(t)
	var (
		missingProgress bool
		pendingOverlays []string
		evidenceGates   []string
	)
	deps := eng.deps()
	deps.ImplementSessionState = func(context.Context, *api.Session) surface.ImplementSessionState {
		return surface.ImplementSessionState{
			WrapupGatesLoaded: true,
			ProgressMissing:   missingProgress,
			PendingOverlayIDs: pendingOverlays,
		}
	}
	deps.CoordinatorFrame = prefixStabilityMutableCoordCtx{
		base:          deps.CoordinatorFrame,
		evidenceGates: &evidenceGates,
	}
	eng.SetDeps(deps)

	sess := &api.Session{
		ID:            "prefix-gate-flips",
		Posture:       api.SessionPostureBuild,
		AgentType:     orchestration.ProfileCoordinator,
		WorkspacePath: t.TempDir(),
	}
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "implement feature"},
	}

	eng.BeginPromptTurn(sess.ID, "")
	eng.SetTurnSurfaceID(sess.ID, "implement_dispatch")
	var firstStandingContent string
	var firstStandingKey string
	firstStandingMarkIdx := -1
	firstHistoryMarkIdx := -1

	type testCase struct {
		name            string
		missingProgress bool
		pendingOverlays []string
		partialHistory  bool
		verifyRequired  bool
	}
	cases := []testCase{
		{name: "baseline", missingProgress: false},
		{name: "progress_missing", missingProgress: true},
		{name: "pending_overlays", pendingOverlays: []string{"job-ov-1"}},
		{name: "partial_jobs_in_history", partialHistory: true},
		{name: "verify_required", verifyRequired: true},
		{name: "all_active", missingProgress: true, pendingOverlays: []string{"job-ov-1"}, partialHistory: true, verifyRequired: true},
		{name: "return_to_baseline", missingProgress: false},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			missingProgress = tc.missingProgress
			pendingOverlays = tc.pendingOverlays
			if tc.verifyRequired {
				evidenceGates = []string{"verify"}
			} else {
				evidenceGates = nil
			}

			testHistory := history
			if tc.partialHistory {
				testHistory = append([]api.Message(nil), history...)
				testHistory = append(testHistory, api.Message{
					Role:          api.MessageRoleAssistant,
					Content:       `<task job_id="j-part" agent_type="implementer" state="partial"></task>`,
					WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "j-part", Status: api.WorkerSummaryStatusPartial},
				})
			}

			eng.Cache().LoadTurn(sess.ID).Iteration = i
			out, err := eng.BuildCompletionMessages(t.Context(), sess, testHistory, nil)
			if err != nil {
				t.Fatalf("BuildCompletionMessages: %v", err)
			}
			turn := eng.Cache().LoadTurn(sess.ID)

			standingIdx := -1
			historyIdx := -1
			for idx, m := range out {
				if m.PromptCacheBreakpoint == api.PromptCacheTierStanding {
					standingIdx = idx
				}
				if m.PromptCacheBreakpoint == api.PromptCacheTierHistory {
					historyIdx = idx
				}
			}

			if i == 0 {
				firstStandingContent = out[0].Content
				firstStandingKey = turn.StablePromptKey
				firstStandingMarkIdx = standingIdx
				firstHistoryMarkIdx = historyIdx
			} else if !tc.partialHistory {
				if out[0].Content != firstStandingContent {
					t.Fatalf("standing content mutated across mid-turn gate flips:\ngot=%q\nwant=%q", out[0].Content, firstStandingContent)
				}
				if turn.StablePromptKey != firstStandingKey {
					t.Fatalf("standing key mutated across mid-turn gate flips: %q != %q", turn.StablePromptKey, firstStandingKey)
				}
				if standingIdx != firstStandingMarkIdx {
					t.Fatalf("standing mark row changed: %d != %d", standingIdx, firstStandingMarkIdx)
				}
				if historyIdx != firstHistoryMarkIdx {
					t.Fatalf("history mark row changed: %d != %d", historyIdx, firstHistoryMarkIdx)
				}
			} else {
				if out[0].Content != firstStandingContent {
					t.Fatalf("standing content mutated with partial history:\ngot=%q\nwant=%q", out[0].Content, firstStandingContent)
				}
				if turn.StablePromptKey != firstStandingKey {
					t.Fatalf("standing key mutated with partial history: %q != %q", turn.StablePromptKey, firstStandingKey)
				}
			}

			var turnStateBlock string
			for idx := historyIdx + 1; idx < len(out); idx++ {
				content := out[idx].Content
				if strings.Contains(content, "No checklist is stored") ||
					strings.Contains(content, "Integrate turn") ||
					strings.Contains(content, "Partial `") ||
					strings.Contains(content, "test-evidence gate") {
					turnStateBlock = content
					break
				}
			}

			if tc.missingProgress && !strings.Contains(turnStateBlock, "No checklist is stored") {
				t.Fatalf("expected checklist warning in turn-state block: %q", turnStateBlock)
			}
			if len(tc.pendingOverlays) > 0 && !strings.Contains(turnStateBlock, "Integrate turn") {
				t.Fatalf("expected integrate-turn in turn-state block: %q", turnStateBlock)
			}
			if tc.partialHistory && len(tc.pendingOverlays) == 0 && !strings.Contains(turnStateBlock, "Partial `j-part`") {
				t.Fatalf("expected partial job in turn-state block: %q", turnStateBlock)
			}
			if tc.verifyRequired && !strings.Contains(turnStateBlock, "test-evidence gate") {
				t.Fatalf("expected test-evidence gate in turn-state block: %q", turnStateBlock)
			}
		})
	}
}

func TestPromptCacheVolatileChangeDoesNotAlterStableBytes(t *testing.T) {
	eng := prefixStabilityEngine(t)
	sess := &api.Session{
		ID:            "prefix-volatile",
		Posture:       api.SessionPostureBuild,
		AgentType:     orchestration.ProfileCoordinator,
		WorkspacePath: t.TempDir(),
	}
	ctx := context.Background()
	eng.BeginPromptTurn(sess.ID, "")
	eng.SetTurnSurfaceID(sess.ID, "implement_dispatch")
	msgs1, err := eng.BuildCompletionMessages(ctx, sess, nil, nil)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	stable1 := eng.Cache().LoadTurn(sess.ID).StablePrompt
	runCtx1 := countCoordinatorRunContextBlocks(msgs1)

	turn := eng.Cache().LoadTurn(sess.ID)
	turn.Iteration = 1
	msgs2, err := eng.BuildCompletionMessages(ctx, sess, nil, nil)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	stable2 := eng.Cache().LoadTurn(sess.ID).StablePrompt
	runCtx2 := countCoordinatorRunContextBlocks(msgs2)
	if runCtx1 != 1 || runCtx2 != 1 {
		t.Fatalf("each request needs the current workflow context: %d -> %d", runCtx1, runCtx2)
	}
	if stable2 != stable1 {
		t.Fatalf("volatile-only turn change must not alter stable bytes")
	}
}

func countCoordinatorRunContextBlocks(msgs []api.Message) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m.Content, inject.ActiveWorkflowInjectSentinel) {
			n++
		}
	}
	return n
}
