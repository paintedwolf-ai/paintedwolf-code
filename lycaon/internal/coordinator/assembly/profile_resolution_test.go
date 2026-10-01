package assembly

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil/prompttest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildCompletionMessages_pinsSynthesisPromptAcrossGroundingRetry(t *testing.T) {
	eng := synthesisPromptStabilityEngine(t)
	sess := &api.Session{
		ID:            "synth-pin",
		Posture:       api.SessionPostureBuild,
		AgentType:     orchestration.ProfileCoordinator,
		WorkspacePath: t.TempDir(),
	}
	ctx := context.Background()
	history := []api.Message{
		{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Kind: api.MessageKindHostLoopWake, Visibility: api.MessageVisibilityInternal, Content: surface.HostLoopWakeSentinel},
		{Role: api.MessageRoleAssistant, Content: "first closeout"},
		{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Kind: api.MessageKindCoordinatorGuidance, HostSignalID: "SYNTH_HANDLE_NOT_IN_LEGS", Visibility: api.MessageVisibilityInternal, Content: "[host:coordinator-citation-grounding]\n\nRejected: x\nCode: SYNTH_HANDLE_NOT_IN_LEGS"},
	}

	eng.BeginPromptTurn(sess.ID, "")
	eng.Cache().SetTurnSurfaceID(sess.ID, "implement_synthesis")

	msgs, err := eng.BuildCompletionMessages(ctx, sess, history, nil)
	if err != nil {
		t.Fatalf("BuildCompletionMessages: %v", err)
	}
	if len(msgs) == 0 || msgs[0].Role != api.MessageRoleSystem {
		t.Fatal("expected system prompt")
	}
	sys := msgs[0].Content
	if !strings.Contains(sys, "## Synthesis turn") {
		t.Fatalf("pinned synthesis surface must render synthesis mode partial; got investigate=%v wrapup=%v",
			strings.Contains(sys, "## Investigate"), strings.Contains(sys, "Read-only report"))
	}
	if strings.Contains(sys, "## Investigate") {
		t.Fatal("must not render investigate mode partial when synthesis surface is pinned")
	}
	if strings.Contains(sys, "INVEST_HANDLE_NOT_OBSERVED") {
		t.Fatal("must not carry investigate citation codes on pinned synthesis surface")
	}
	if !strings.Contains(sys, "SYNTH_HANDLE_NOT_IN_LEGS") {
		t.Fatal("synthesis prompt must document SYNTH_HANDLE_NOT_IN_LEGS")
	}
}

func synthesisPromptStabilityEngine(t *testing.T) *AssemblyEngine {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
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
		ImplementSessionState: func(_ context.Context, _ *api.Session) surface.ImplementSessionState {
			return surface.WithWrapupGates(surface.ImplementSessionState{}, true, false)
		},
	}
	eng := &AssemblyEngine{}
	eng.SetDeps(deps)
	return eng
}
