package promptrender_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

type noopNudgeFormatter struct{}

func (noopNudgeFormatter) FormatNudge(_ context.Context, code string, _ map[string]any) (string, error) {
	return code, nil
}

type staticCoordFrame struct{ ctx api.CoordinatorRunContext }

func (f staticCoordFrame) BuildCoordinatorTurnFrame(context.Context, string, *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: f.ctx}, nil
}

type staticSelection struct {
	loaded map[string]bool
}

func (s staticSelection) LoadedTools(string) map[string]bool {
	out := make(map[string]bool, len(s.loaded))
	for k, v := range s.loaded {
		out[k] = v
	}
	return out
}

func (staticSelection) FullCatalog(string) bool { return false }

func ensureWorkspaceTopologyDirs(baseDir string, rootCount int, primaryGit, anyGit bool) ([]map[string]any, int, string, int) {
	if rootCount <= 0 || strings.TrimSpace(baseDir) == "" {
		return nil, 0, "", 0
	}
	gitDir := filepath.Join(baseDir, "git-root")
	nonGitDir := filepath.Join(baseDir, "nongit-root")
	secAlt := filepath.Join(baseDir, "nongit-sec")
	for _, dir := range []string{filepath.Join(gitDir, ".git"), nonGitDir, secAlt} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, 0, "", 0
		}
	}

	primaryPath := nonGitDir
	if primaryGit {
		primaryPath = gitDir
	}
	roots := []map[string]any{{
		"label":      "main",
		"path":       primaryPath,
		"is_primary": true,
		"is_active":  true,
		"has_git":    primaryGit,
	}}
	if rootCount > 1 {
		secGit := anyGit && !primaryGit
		var secPath string
		if secGit {
			secPath = gitDir
		} else {
			secPath = secAlt
		}
		roots = append(roots, map[string]any{
			"label":      "secondary",
			"path":       secPath,
			"is_primary": false,
			"is_active":  false,
			"has_git":    secGit,
		})
	}
	return roots, len(roots), primaryPath, 0
}

func loadedSetFromOffered(offered []string) map[string]bool {
	m := make(map[string]bool, len(offered))
	for _, name := range offered {
		m[name] = true
	}
	return m
}

func buildRecipientEngine(
	s *promptrender.Stock,
	runCtx api.CoordinatorRunContext,
	auth hoststate.TurnAuthority,
	hasGit bool,
	jobs []bgprocess.JobSnapshot,
) *assembly.AssemblyEngine {
	pe := s.Engine()
	deps := assembly.AssemblyDeps{
		Prompts: pe,
		Injects: prompts.NewInjectRenderer(pe),
		TurnReader: func(context.Context, *api.Session, []api.Message, inject.CoordinatorTurnFrame) (prompts.Reader, error) {
			return auth.Reader, nil
		},
		Limits:           func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: staticCoordFrame{ctx: runCtx},
		Selection:        staticSelection{loaded: loadedSetFromOffered(auth.Offered)},
		WebSearchEnabled: func() bool { return true },
		WorkspaceRoots: func(_ context.Context, sess *api.Session) ([]map[string]any, int, string, int) {
			if sess == nil || strings.TrimSpace(sess.WorkspacePath) == "" {
				return nil, 0, "", 0
			}
			return ensureWorkspaceTopologyDirs(sess.WorkspacePath, 1, hasGit, hasGit)
		},
		CommandJobs: func(string) []bgprocess.JobSnapshot { return slices.Clone(jobs) },
	}
	eng := &assembly.AssemblyEngine{}
	eng.SetDeps(deps)
	return eng
}

// newlyDeliveredHostInstructions extracts only host-authored system/inject blocks
// and target-turn rejected tool guidance, excluding user content, assistant
// messages, earlier historical observations, and tool schema listings.
func newlyDeliveredHostInstructions(msgs []api.Message, targetRejectID string) []string {
	isHost := func(origin api.MessageOrigin, authority api.ContentAuthority) bool {
		return origin == api.MessageOriginHost || authority == api.ContentAuthoritySystem || authority == api.ContentAuthorityDeveloper
	}
	var out []string
	for _, m := range msgs {
		if m.ID == targetRejectID && m.ToolResult != nil && m.ToolResult.Outcome == api.ToolResultOutcomeRejected {
			out = append(out, m.Content)
			continue
		}
		if len(m.ContentParts) > 0 {
			for _, part := range m.ContentParts {
				if isHost(part.Origin, part.Authority) {
					out = append(out, part.Content)
				}
			}
			continue
		}
		if m.Role == api.MessageRoleSystem || isHost(m.Origin, m.Authority) {
			out = append(out, m.Content)
		}
	}
	return out
}

func parsedCallsForTool(cat *toolvocab.Catalog, blocks []string, wantTool string) []toolvocabtest.CallExample {
	var out []toolvocabtest.CallExample
	for _, b := range blocks {
		for _, ex := range toolvocabtest.ParseCallExamples(cat, b) {
			if ex.Tool == wantTool {
				out = append(out, ex)
			}
		}
	}
	return out
}

// The standing system prompt leads every coordinator request and stays pinned,
// on a sparse held-host turn and on a broad turn with a live job.
func TestCoordinatorStandingPromptLeadsAndIsPinned(t *testing.T) {
	t.Cleanup(hoststate.HoldAuthorityMemos())
	s := mustStock(t)
	coordStates, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}
	wsDir := filepath.Join(t.TempDir(), "project")

	sparseSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
			c.TurnProfile.SurfaceID == "await_host" && c.Loading == hoststate.LoadingFloorOnly &&
			c.RootTopology == hoststate.RootsSingleGit
	})
	broadSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
			c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
			c.RootTopology == hoststate.RootsSingleGit
	})
	for _, tc := range []struct {
		st     hoststate.CoordinatorState
		runCtx api.CoordinatorRunContext
		jobs   []bgprocess.JobSnapshot
	}{
		{sparseSt, api.CoordinatorRunContext{
			RunID: "run-1", WorkflowID: "implement", CurrentPhase: "await_host",
			PhaseCoordinatorSurface: "await_host", PhaseHostHeld: true,
		}, nil},
		{broadSt, api.CoordinatorRunContext{
			RunID: "run-2", WorkflowID: "implement", CurrentPhase: "work",
			PhaseCoordinatorSurface: "implement_investigate", AllowedAgents: spawn.AmbientAllowedAgents(),
		}, fixedJobSnapshots(occRunning, jobClock)},
	} {
		auth, err := hoststate.FreezeTurnAuthority(t.Context(), tc.st)
		if err != nil {
			t.Fatalf("FreezeTurnAuthority(%s): %v", tc.st.ID, err)
		}
		eng := buildRecipientEngine(s, tc.runCtx, auth, true, tc.jobs)
		sess := &api.Session{ID: "standing-" + tc.st.ID, AgentType: orchestration.ProfileCoordinator, Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild, WorkspacePath: wsDir}
		eng.BeginPromptTurn(sess.ID, "")
		msgs, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "Check status."}}, nil)
		if err != nil {
			t.Fatalf("BuildCompletionMessages(%s): %v", tc.st.ID, err)
		}
		if len(msgs) < 2 || msgs[0].Role != api.MessageRoleSystem || !msgs[0].ContextPinned {
			t.Fatalf("%s: standing system prompt is not first and pinned", tc.st.ID)
		}
		if viols := promptrender.EvaluateClaimsForTest(s, "standing ("+tc.st.ID+")", msgs[0].Content, auth, api.SessionModeAgent, api.SessionPostureBuild, toolvocabtest.SubjectSelfNow(), true); len(viols) != 0 {
			t.Fatalf("%s standing system prompt claim violations: %v", tc.st.ID, viols)
		}
	}
}
