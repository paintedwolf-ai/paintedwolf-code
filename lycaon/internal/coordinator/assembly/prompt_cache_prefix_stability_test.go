package assembly

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
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
	if _, err := eng.BuildCompletionMessages(ctx, sess, nil, nil); err != nil {
		t.Fatalf("build: %v", err)
	}
	keyBuild := eng.Cache().LoadTurn(sess.ID).StablePromptKey

	sess.Posture = api.SessionPostureSpec
	turn := eng.Cache().LoadTurn(sess.ID)
	turn.Iteration = 0
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

func TestStableCapabilityFingerprintCoversRenderedCapabilityInputs(t *testing.T) {
	vars := map[string]any{"caps": map[string]any{"vision": false}}
	roster := &inject.AgentRoster{Effective: []string{"investigate", "implement"}}
	base := stableCapabilityFingerprint(vars, roster, true)

	vars["caps"].(map[string]any)["vision"] = true
	vision := stableCapabilityFingerprint(vars, roster, true)
	if vision == base {
		t.Fatal("vision change did not invalidate stable capability fingerprint")
	}
	if got := stableCapabilityFingerprint(vars, roster, false); got == vision {
		t.Fatal("web-search change did not invalidate stable capability fingerprint")
	}
	roster.Effective = []string{"implement"}
	if got := stableCapabilityFingerprint(vars, roster, true); got == vision {
		t.Fatal("effective agent roster change did not invalidate stable capability fingerprint")
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
	if runCtx2 >= runCtx1 && runCtx1 > 0 {
		t.Fatalf("expected volatile run-context count to drop on iteration 2: %d -> %d", runCtx1, runCtx2)
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
