package assembly

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/prompttest"
	"github.com/lycaon/lycaon/internal/testutil/surfacetest"
	"github.com/lycaon/lycaon/internal/toolcontract"
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
	if strings.Contains(sys, "## Investigate") {
		t.Fatal("must not render investigate mode partial when synthesis surface is pinned")
	}
	if strings.Contains(sys, "INVEST_HANDLE_NOT_OBSERVED") {
		t.Fatal("must not carry investigate citation codes on pinned synthesis surface")
	}
	if !strings.Contains(sys, "tool results in this chat") {
		t.Fatal("synthesis prompt must carry its citation section")
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
			return surfacetest.WithWrapupGates(surface.ImplementSessionState{}, true, false)
		},
	}
	eng := &AssemblyEngine{}
	eng.SetDeps(deps)
	return eng
}

// A turn that moves to another surface mid-turn is read with that surface's
// tools, not the ones it opened with.
func TestBuildCompletionMessagesReadsAMovedTurnWithItsNewSurface(t *testing.T) {
	eng := synthesisPromptStabilityEngine(t)
	sess := &api.Session{ID: "moved-turn", Posture: api.SessionPostureBuild, AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir()}
	history := []api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "build parallel modules"}}
	eng.BeginPromptTurn(sess.ID, "")
	for _, id := range []string{"implement_investigate", surface.SurfaceImplementPark} {
		eng.Cache().SetTurnSurfaceID(sess.ID, id)
		if _, err := eng.BuildCompletionMessages(context.Background(), sess, history, nil); err != nil {
			t.Fatalf("BuildCompletionMessages on %s: %v", id, err)
		}
		if got := eng.Cache().LoadTurn(sess.ID).Reader.SurfaceID(); got != id {
			t.Fatalf("turn pinned to %s is read as %s", id, got)
		}
	}
}

func TestTurnReaderAndWorkerReaderSpecificityAcrossGitRootCountAndUpgrade(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	gitDir := t.TempDir()
	testutil.FailErr(t, "create .git dir", mkdirAllGit(gitDir))
	plainDir := t.TempDir()

	// 1. turnReader varies HasGit and RootCount one dimension at a time.
	for _, tc := range []struct {
		name     string
		roots    []map[string]any
		count    int
		active   string
		wantGit  bool
		wantText string
		forbid   string
	}{
		{
			name:     "single_git_root",
			roots:    []map[string]any{{"label": "app", "path": gitDir, "is_primary": true, "is_active": true}},
			count:    1,
			active:   gitDir,
			wantGit:  true,
			wantText: "inspect Git status, diffs, and history with read-only native commands",
			forbid:   "in Git-backed folders",
		},
		{
			name:     "single_non_git_root",
			roots:    []map[string]any{{"label": "app", "path": plainDir, "is_primary": true, "is_active": true}},
			count:    1,
			active:   plainDir,
			wantGit:  false,
			wantText: "Use `command` to read, search, or query project files with read-only native commands",
			forbid:   "Git status",
		},
		{
			name: "multi_root_with_git",
			roots: []map[string]any{
				{"label": "app", "path": gitDir, "is_primary": true, "is_active": true},
				{"label": "docs", "path": plainDir, "is_primary": false, "is_active": false},
			},
			count:    2,
			active:   gitDir,
			wantGit:  true,
			wantText: "inspect Git status, diffs, and history in Git-backed folders",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := &promptSurface{
				deps: promptSurfaceDeps{
					Prompts: pe,
					WorkspaceRoots: func(context.Context, *api.Session) ([]map[string]any, int, string, int) {
						return tc.roots, tc.count, tc.active, 0
					},
				},
				cache: &SessionPromptCache{},
			}
			sess := &api.Session{
				ID:            "ask-" + tc.name,
				Mode:          api.SessionModeAsk,
				Posture:       api.SessionPostureBuild,
				AgentType:     orchestration.ProfileCoordinator,
				WorkspacePath: tc.active,
			}
			turn := &TurnAssemblyScratch{SurfaceID: "observe_investigate"}
			var frame inject.CoordinatorTurnFrame
			reader, err := ps.turnReader(t.Context(), sess, frame, nil, turn, tc.count)
			testutil.FailErr(t, "turnReader", err)
			facts := reader.Facts()
			if facts["has_git"] != tc.wantGit || reader.RootCount() != tc.count {
				t.Fatalf("facts has_git=%v root_count=%d, want %t/%d", facts["has_git"], reader.RootCount(), tc.wantGit, tc.count)
			}
			rendered, err := ps.renderSystemPrefix(t.Context(), sess, frame, nil, turn, nil)
			testutil.FailErr(t, "renderSystemPrefix", err)
			if !strings.Contains(rendered, tc.wantText) {
				t.Fatalf("rendered prompt missing %q:\n%s", tc.wantText, rendered)
			}
			if tc.forbid != "" && strings.Contains(rendered, tc.forbid) {
				t.Fatalf("rendered prompt leaked %q:\n%s", tc.forbid, rendered)
			}
			for _, leak := range []string{"{%", "{{", "&quot;", "&#39;"} {
				if strings.Contains(rendered, leak) {
					t.Fatalf("rendered prompt leaked template syntax %q:\n%s", leak, rendered)
				}
			}
		})
	}

	// 2. workerReader for read-only workers keeps read-only command guidance even after root upgrades to Agent mode.
	loadedProfiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	for _, agentType := range []string{"path-explorer", "repo-researcher"} {
		ps := &promptSurface{
			deps: promptSurfaceDeps{
				Prompts: pe,
				WorkspaceRoots: func(context.Context, *api.Session) ([]map[string]any, int, string, int) {
					return []map[string]any{{"label": "app", "path": gitDir, "is_primary": true, "is_active": true}}, 1, gitDir, 0
				},
			},
			cache: &SessionPromptCache{},
		}
		workerSess := &api.Session{
			ID:              "worker-" + agentType,
			ParentSessionID: "root-upgraded-to-agent",
			RootSessionID:   "root-upgraded-to-agent",
			Mode:            api.SessionModeAgent,
			Posture:         api.SessionPostureBuild,
			AgentType:       agentType,
			WorkspacePath:   gitDir,
		}
		reader, err := ps.workerReader(t.Context(), workerSess, pe, agentType, map[string]any{
			"visible_tools": []string{"command", "command_output", "command_stop", "summarize"},
		})
		testutil.FailErr(t, "workerReader "+agentType, err)
		wantReadOnly, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
		testutil.FailErr(t, "NewCommandEffects", err)
		if reader.Effects() != wantReadOnly {
			t.Fatalf("%s effects after root upgrade = %v, want CommandReadOnly", agentType, reader.Effects())
		}
		vars := reader.Facts()
		for _, flag := range []string{"profile_command_mutation", "profile_command_external_exec", "profile_command_network_egress"} {
			if vars[flag] != false {
				t.Fatalf("%s %s = %v after root upgrade, want false", agentType, flag, vars[flag])
			}
		}
		_ = loadedProfiles
	}
}

func mkdirAllGit(dir string) error {
	return os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
}
