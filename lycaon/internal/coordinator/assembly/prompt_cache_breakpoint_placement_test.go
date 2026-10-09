package assembly

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil/prompttest"
	"github.com/lycaon/lycaon/pkg/api"
)

type placementWorkerCtx struct {
	leg inject.WorkerLegContext
}

func (s placementWorkerCtx) BuildWorkerPromptContext(_ string, _ *api.Session) (inject.WorkerLegContext, error) {
	return s.leg, nil
}

func placementTestRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

func assertPromptCacheBreakpointBeforeVolatile(t *testing.T, msgs []api.Message, requireMarker bool) {
	t.Helper()
	breakIdx := promptCacheMarkedIndex(msgs)
	if !requireMarker {
		if breakIdx < 0 {
			return
		}
	} else if breakIdx < 0 {
		t.Fatal("expected PromptCacheBreakpoint on a stable block")
	}
	firstVolatile := firstVolatileBlockIndex(msgs)
	if firstVolatile >= 0 && breakIdx >= firstVolatile {
		t.Fatalf("breakpoint index %d must precede first volatile block at %d", breakIdx, firstVolatile)
	}
	var marks []int
	for i, m := range msgs {
		if m.PromptCacheBreakpoint == api.PromptCacheTierNone {
			continue
		}
		if i > breakIdx {
			t.Fatalf("volatile message at %d must not carry PromptCacheBreakpoint", i)
		}
		marks = append(marks, i)
	}
	if len(marks) > 2 {
		t.Fatalf("breakpoints = %v, want at most the standing boundary and the history tail", marks)
	}
	if msgs[marks[0]].PromptCacheBreakpoint != api.PromptCacheTierStanding {
		t.Fatalf("the first boundary closes %q, want the standing prefix", msgs[marks[0]].PromptCacheBreakpoint)
	}
}

func TestMarkPromptCacheBreakpointsNamesEachTier(t *testing.T) {
	out := []api.Message{{Role: api.MessageRoleSystem}, {Role: api.MessageRoleUser}, {Role: api.MessageRoleTool}}
	markPromptCacheBreakpoints(out, 0, 2)
	if out[0].PromptCacheBreakpoint != api.PromptCacheTierStanding || out[1].PromptCacheBreakpoint != api.PromptCacheTierNone || out[2].PromptCacheBreakpoint != api.PromptCacheTierHistory {
		t.Fatalf("marks = %q %q %q", out[0].PromptCacheBreakpoint, out[1].PromptCacheBreakpoint, out[2].PromptCacheBreakpoint)
	}
	// With no history the one boundary closes the standing prefix.
	lone := []api.Message{{Role: api.MessageRoleSystem}}
	markPromptCacheBreakpoints(lone, 0, 0)
	if lone[0].PromptCacheBreakpoint != api.PromptCacheTierStanding {
		t.Fatalf("lone mark = %q", lone[0].PromptCacheBreakpoint)
	}
	// Out-of-range indexes mark nothing.
	markPromptCacheBreakpoints(lone, -1, 7)
}

// promptCacheMarkedIndex is the last marked row: the end of stable history.
func promptCacheMarkedIndex(msgs []api.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].PromptCacheBreakpoint != api.PromptCacheTierNone {
			return i
		}
	}
	return -1
}

func firstVolatileBlockIndex(msgs []api.Message) int {
	for i, m := range msgs {
		if isVolatilePromptBlock(m.Content) {
			return i
		}
	}
	return -1
}

func isVolatilePromptBlock(content string) bool {
	return strings.Contains(content, inject.ActiveWorkflowInjectSentinel) ||
		strings.Contains(content, inject.ImplementSpawnInjectSentinel) ||
		strings.Contains(content, inject.WorkerLegInjectSentinel) ||
		strings.Contains(content, inject.BoardOrientationInjectSentinel) ||
		strings.Contains(content, packboard.PackBoardSentinel)
}

func TestPromptCacheBreakpointPlacementTable(t *testing.T) {
	root := placementTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	inj := prompts.NewInjectRenderer(pe)

	cases := []struct {
		name          string
		deps          AssemblyDeps
		sess          *api.Session
		requireMarker bool
	}{
		{
			name: "implement_build_coordinator",
			deps: AssemblyDeps{
				Prompts: pe,
				Injects: inj,
				Limits:  func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
				CoordinatorFrame: prefixStabilityCoordCtx{ctx: api.CoordinatorRunContext{
					AllowedAgents: spawn.AmbientAllowedAgents(),
				}},
				PromptToolLister: prompttest.CoordinatorTools,
				WorkspaceRoots:   prefixStabilityWorkspaceRoots(),
			},
			sess: &api.Session{
				ID: "bp-build", Posture: api.SessionPostureBuild,
				AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir(),
			},
			requireMarker: true,
		},
		{
			name: "workflow_session_run_context",
			deps: AssemblyDeps{
				Prompts: pe,
				Injects: inj,
				Limits:  func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
				CoordinatorFrame: prefixStabilityCoordCtx{ctx: api.CoordinatorRunContext{
					WorkflowID: "implement", CurrentPhase: "work",
					AllowedAgents: spawn.AmbientAllowedAgents(),
				}},
				PromptToolLister: prompttest.CoordinatorTools,
				WorkspaceRoots:   prefixStabilityWorkspaceRoots(),
			},
			sess: &api.Session{
				ID: "bp-workflow", Posture: api.SessionPostureBuild,
				AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir(),
			},
			requireMarker: true,
		},
		{
			name: "worker_child_leg_inject",
			deps: func() AssemblyDeps {
				boardEng := NewBoardEngine(placementStubBoardBuilder{hash: "worker-board"}, nil, nil)
				boardEng.SetInjectRenderer(inj)
				return AssemblyDeps{
					Prompts:       pe,
					Injects:       inj,
					Limits:        func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
					WorkerContext: placementWorkerCtx{leg: inject.WorkerLegContext{LegID: "leg-1", Checklist: []string{"step"}}},
					Board:         boardEng,
				}
			}(),
			sess: &api.Session{
				ID: "bp-worker", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
				Posture: api.SessionPostureBuild, AgentType: "implementer", WorkspacePath: t.TempDir(),
			},
			requireMarker: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := &AssemblyEngine{}
			eng.SetDeps(tc.deps)
			eng.BeginPromptTurn(tc.sess.ID, "")
			msgs, err := eng.BuildCompletionMessages(context.Background(), tc.sess, []api.Message{
				{Role: api.MessageRoleUser, Content: "continue"},
			}, nil)
			if err != nil {
				t.Fatalf("BuildCompletionMessages: %v", err)
			}
			assertPromptCacheBreakpointBeforeVolatile(t, msgs, tc.requireMarker)
		})
	}
}

type placementStubBoardBuilder struct{ hash string }

func (s placementStubBoardBuilder) BuildBoardSnapshot(_ context.Context, _, _, _ string, _ api.BoardDetailLevel, _ []projectroot.RootRef) (*api.BoardSnapshot, error) {
	return &api.BoardSnapshot{PackContentHash: s.hash}, nil
}
