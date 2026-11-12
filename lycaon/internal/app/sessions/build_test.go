package sessions

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/pkg/api"
)

// gitRepoDir creates a directory gitrepo.Discover recognizes as a repository:
// Discover only needs a .git entry somewhere on the walked path.
func gitRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("create .git entry: %v", err)
	}
	return dir
}

// skillSurfaceVarsInput maps the invocation context onto the skill reader's
// surface facts: the agent's tool profile, the turn surface, the offered
// tools, and the workspace shape (worker branch, git, root count).
func TestSkillSurfaceVarsInputMapsReaderFacts(t *testing.T) {
	gitDir := gitRepoDir(t)
	plainDir := t.TempDir()
	budget := spawn.WorkerToolBudget{Default: 9, Min: 2, Max: 21}
	for _, tc := range []struct {
		name string
		tctx tools.ToolContext
		want prompts.SkillSurfaceVarsInput
	}{
		{
			name: "coordinator on a worker branch with git and two roots",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: "s"},
				Source: tools.InvocationSource{
					Roots: []projectroot.RootRef{
						{ID: "main", Path: gitDir, IsPrimary: true},
						{ID: "lib", Path: plainDir},
					},
					ActiveRootID:        "main",
					SourceWorkspaceKind: api.SourceWorkspaceKindWorker,
					WorkerBranchRoot:    plainDir,
				},
				Turn: tools.InvocationTurn{TurnSurfaceID: " implement_routing ", TurnOfferedToolNames: []string{"recall", "task"}},
			},
			want: prompts.SkillSurfaceVarsInput{
				ProfileID:        "coordinator",
				SurfaceID:        "implement_routing",
				Offered:          []string{"recall", "task"},
				OnWorkerBranch:   true,
				HasGit:           true,
				RootCount:        2,
				DelegationBudget: budget,
			},
		},
		{
			name: "unregistered agent keeps its id and reports no workspace facts",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "mystery-agent"},
				Source: tools.InvocationSource{
					SourceWorkspaceKind: api.SourceWorkspaceKindWorker,
				},
			},
			want: prompts.SkillSurfaceVarsInput{
				ProfileID:        "mystery-agent",
				DelegationBudget: budget,
			},
		},
		{
			name: "worker branch facts need the worker source kind",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "implementer"},
				Source: tools.InvocationSource{
					Roots:            []projectroot.RootRef{{ID: "main", Path: plainDir, IsPrimary: true}},
					WorkerBranchRoot: gitDir,
				},
			},
			want: prompts.SkillSurfaceVarsInput{
				ProfileID:        "implement",
				HasGit:           true,
				RootCount:        1,
				DelegationBudget: budget,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := skillSurfaceVarsInput(tc.tctx, budget)
			want := tc.want
			if got.ProfileID != want.ProfileID || got.SurfaceID != want.SurfaceID ||
				got.OnWorkerBranch != want.OnWorkerBranch || got.HasGit != want.HasGit ||
				got.RootCount != want.RootCount || got.DelegationBudget != want.DelegationBudget ||
				!slices.Equal(got.Offered, want.Offered) {
				t.Fatalf("skillSurfaceVarsInput = %+v, want %+v", got, want)
			}
		})
	}
}

// skillReaderHasGit reports the first usable git repository: the active root,
// then the worker branch root, then any attached root, skipping blank paths.
func TestSkillReaderHasGitPrefersActiveRootThenBranchThenRoots(t *testing.T) {
	gitDir := gitRepoDir(t)
	plainDir := t.TempDir()
	for _, tc := range []struct {
		name string
		tctx tools.ToolContext
		want bool
	}{
		{
			name: "active root is a repository",
			tctx: tools.ToolContext{Source: tools.InvocationSource{
				Roots:        []projectroot.RootRef{{ID: "main", Path: gitDir, IsPrimary: true}},
				ActiveRootID: "main",
			}},
			want: true,
		},
		{
			name: "worker branch root is a repository",
			tctx: tools.ToolContext{Source: tools.InvocationSource{
				Roots:            []projectroot.RootRef{{ID: "main", Path: plainDir, IsPrimary: true}},
				ActiveRootID:     "main",
				WorkerBranchRoot: gitDir,
			}},
			want: true,
		},
		{
			name: "non-active attached root is a repository",
			tctx: tools.ToolContext{Source: tools.InvocationSource{
				Roots: []projectroot.RootRef{
					{ID: "main", Path: plainDir, IsPrimary: true},
					{Path: "   "},
					{ID: "lib", Path: gitDir},
				},
				ActiveRootID: "main",
			}},
			want: true,
		},
		{
			name: "no repository anywhere",
			tctx: tools.ToolContext{Source: tools.InvocationSource{
				Roots:        []projectroot.RootRef{{ID: "main", Path: plainDir, IsPrimary: true}},
				ActiveRootID: "main",
			}},
			want: false,
		},
		{
			name: "blank directories are skipped",
			tctx: tools.ToolContext{Source: tools.InvocationSource{
				WorkerBranchRoot: "   ",
			}},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := skillReaderHasGit(tc.tctx); got != tc.want {
				t.Fatalf("skillReaderHasGit = %t, want %t", got, tc.want)
			}
		})
	}
}

// skillReaderOfferedTools checks skill reach against the turn's offered tools,
// widened on coordinator surfaces by the surface's full addressable set, and
// adds command mutation reach only when the reader's scope allows it.
func TestSkillReaderOfferedTools(t *testing.T) {
	for _, tc := range []struct {
		name         string
		tctx         tools.ToolContext
		wantExact    map[string]bool
		wantContains []string
		wantMissing  []string
	}{
		{
			name: "turn tools only outside coordinator surfaces",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "path-explorer"},
				Turn:     tools.InvocationTurn{TurnOfferedToolNames: []string{"recall", "summarize"}},
			},
			wantExact: map[string]bool{"recall": true, "summarize": true},
		},
		{
			name: "coordinator surface merges its addressable set",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "coordinator"},
				Turn: tools.InvocationTurn{
					TurnSurfaceID:        "implement_routing",
					TurnOfferedToolNames: []string{"custom_probe"},
				},
			},
			wantContains: []string{"custom_probe", "recall", "command", "command_output", "command_stop"},
			wantMissing:  []string{toolvocab.ReachCommandMutation},
		},
		{
			name: "coordinator without a surface keeps the turn tools",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "coordinator"},
				Turn:     tools.InvocationTurn{TurnOfferedToolNames: []string{"recall"}},
			},
			wantExact: map[string]bool{"recall": true},
		},
		{
			name: "coordinator scope allows command mutation",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "coordinator"},
				Turn:     tools.InvocationTurn{TurnOfferedToolNames: []string{"command"}},
			},
			wantContains: []string{toolvocab.ReachCommandMutation},
		},
		{
			name: "worker profile scope allows command mutation",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "implementer"},
				Turn:     tools.InvocationTurn{TurnOfferedToolNames: []string{"command"}},
			},
			wantContains: []string{toolvocab.ReachCommandMutation},
		},
		{
			name: "read-only profile scope forbids command mutation",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "path-explorer"},
				Turn:     tools.InvocationTurn{TurnOfferedToolNames: []string{"command"}},
			},
			wantExact: map[string]bool{"command": true},
		},
		{
			name: "read-only coordinator surface forbids command mutation",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "coordinator"},
				Turn: tools.InvocationTurn{
					TurnSurfaceID:        "implement_routing",
					TurnOfferedToolNames: []string{"command"},
				},
			},
			wantContains: []string{"command", "recall"},
			wantMissing:  []string{toolvocab.ReachCommandMutation},
		},
		{
			name: "unknown scope falls open and skips the surface merge",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "coordinator"},
				Turn: tools.InvocationTurn{
					TurnSurfaceID:        "no_such_surface",
					TurnOfferedToolNames: []string{"command"},
				},
			},
			wantContains: []string{"command", toolvocab.ReachCommandMutation},
			wantMissing:  []string{"recall"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			offered := skillReaderOfferedTools(tc.tctx)
			if tc.wantExact != nil && !maps.Equal(offered, tc.wantExact) {
				t.Fatalf("skillReaderOfferedTools = %v, want exactly %v", offered, tc.wantExact)
			}
			for _, name := range tc.wantContains {
				if !offered[name] {
					t.Fatalf("skillReaderOfferedTools is missing %q: %v", name, offered)
				}
			}
			for _, name := range tc.wantMissing {
				if offered[name] {
					t.Fatalf("skillReaderOfferedTools unexpectedly holds %q: %v", name, offered)
				}
			}
		})
	}
}

// profileIDForSurface resolves the reader's scope id: the agent's registered
// tool profile, or the agent id itself when unregistered.
func TestProfileIDForSurfaceMapsAgentToProfile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent string
		want  string
	}{
		{name: "coordinator agent", agent: "coordinator", want: "coordinator"},
		{name: "worker agent maps to its tool profile", agent: "path-explorer", want: "explore_readonly"},
		{name: "unregistered agent keeps its id", agent: "mystery-agent", want: "mystery-agent"},
		{name: "agent id is trimmed", agent: "  coordinator  ", want: "coordinator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tctx := tools.ToolContext{Identity: tools.InvocationIdentity{Agent: tc.agent}}
			if got := profileIDForSurface(tctx); got != tc.want {
				t.Fatalf("profileIDForSurface(%q) = %q, want %q", tc.agent, got, tc.want)
			}
		})
	}
}

// newWiredSkillHost builds a native runtime and a bare session host wired by
// the build tool-source wiring, with the stock pack catalog installed as the
// device catalog so profile skill loads resolve shipped pack skills. It also
// creates a root session, whose default coordinator agent gives the skill
// selector its shipped all-skills policy.
func newWiredSkillHost(t *testing.T, workerToolBudgetFor func(string) spawn.WorkerToolBudget) (*toolhost.Runtime, string) {
	t.Helper()
	cat := extpackstest.StockCatalog(t)
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{
		ConfigRoot: configlayout.FindModuleRoot(),
		Catalog:    cat,
	})
	testutil.FailErr(t, "build native runtime", err)
	st := store.NewMemory()
	mgr := session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	root := configlayout.FindModuleRoot()
	mgr.SetEffectiveCatalogDeps(root, cat, nil)
	mgr.Catalog.SetCatalogViewCache(catalogview.NewCache(root, slog.Default()))
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create probe session", err)
	extpacks.SetActive(cat)
	t.Cleanup(extpacks.ClearActive)
	wireSessionToolSources(mgr, rt, nil, workerToolBudgetFor)
	return rt, sess.ID
}

// The wired skills catalog serves the stock pack skills a reader can reach:
// the coordinator surface reader reads a skill whose tools the surface
// addresses, while a reader holding only recall cannot reach it.
func TestWiredSkillsCatalogFiltersStockSkillsByReaderReach(t *testing.T) {
	rt, sessionID := newWiredSkillHost(t, nil)
	coordinator := tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sessionID},
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "main", Path: gitRepoDir(t), IsPrimary: true}},
			ActiveRootID: "main",
		},
		Turn: tools.InvocationTurn{
			TurnSurfaceID:        "implement_routing",
			TurnOfferedToolNames: []string{"recall", "task"},
		},
	}
	out, err := rt.Registry.Run(t.Context(), "skills_read", map[string]any{"need": "research-current-information"}, coordinator)
	testutil.FailErr(t, "read reachable stock skill", err)
	if !strings.Contains(out, "Skill: research-current-information") || !strings.Contains(out, "Research current information") {
		t.Fatalf("coordinator surface read lost the stock skill body:\n%s", out)
	}

	// research-current-information needs web_search or fetch_url; a reader
	// offered only recall cannot reach it, so the read either rejects the need
	// as unknown or answers with a discovery page. Both must omit the skill
	// body, which only a reachable skill renders.
	restricted := coordinator
	restricted.Identity.Agent = "mystery-agent"
	restricted.Turn.TurnSurfaceID = ""
	restricted.Turn.TurnOfferedToolNames = []string{"recall"}
	out, err = rt.Registry.Run(t.Context(), "skills_read", map[string]any{"need": "research-current-information"}, restricted)
	if err != nil {
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "SKILL_UNKNOWN" {
			testutil.FailErr(t, "run skills_read for restricted reader", err)
		}
		out = ""
	}
	if strings.Contains(out, "Research current information") {
		t.Fatalf("restricted reader reached an unreachable skill:\n%s", out)
	}
}

// The wired skill template vars render the caller shape (coordinator, worker
// branch, git, root count) on coordinator surfaces and the delegation budget
// from the host's worker tool budget source elsewhere.
func TestWiredSkillTemplateVarsRenderCallerShapeAndBudget(t *testing.T) {
	rt, sessionID := newWiredSkillHost(t, nil)
	coordinator := tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sessionID},
		Source: tools.InvocationSource{
			Roots: []projectroot.RootRef{
				{ID: "main", Path: gitRepoDir(t), IsPrimary: true},
				{ID: "lib", Path: t.TempDir()},
			},
			ActiveRootID:        "main",
			SourceWorkspaceKind: api.SourceWorkspaceKindWorker,
			WorkerBranchRoot:    t.TempDir(),
		},
		Turn: tools.InvocationTurn{
			TurnSurfaceID:        "implement_routing",
			TurnOfferedToolNames: []string{"recall", "task"},
		},
	}
	body := "coord:{% if is_coordinator %}yes{% else %}no{% endif %} branch:{% if on_worker_branch %}yes{% else %}no{% endif %} git:{% if has_git %}yes{% else %}no{% endif %} roots:{{ root_count }}"
	out, err := rt.Skills.RenderSkillBody(t.Context(), coordinator, skills.Skill{Name: "shape-probe", Body: body})
	testutil.FailErr(t, "render coordinator surface skill body", err)
	if want := "coord:yes branch:yes git:yes roots:2"; out != want {
		t.Fatalf("coordinator surface render = %q, want %q", out, want)
	}

	// An unregistered reader has no tool surface, so the vars fall back to the
	// spawn policy vars carrying the host's default delegation budget.
	unknown := tools.ToolContext{Identity: tools.InvocationIdentity{Agent: "mystery-agent", SessionID: sessionID}}
	out, err = rt.Skills.RenderSkillBody(t.Context(), unknown, skills.Skill{Name: "budget-probe", Body: "budget:{{ worker_tool_budget_default }}"})
	testutil.FailErr(t, "render fallback skill body", err)
	if want := fmt.Sprintf("budget:%d", spawn.DefaultWorkerToolBudget().Default); out != want {
		t.Fatalf("fallback render = %q, want %q", out, want)
	}

	// A host-provided budget source replaces the default for readers without
	// their own surface.
	custom, _ := newWiredSkillHost(t, func(string) spawn.WorkerToolBudget {
		return spawn.WorkerToolBudget{Default: 7, Min: 2, Max: 9}
	})
	out, err = custom.Skills.RenderSkillBody(t.Context(), unknown, skills.Skill{Name: "budget-probe", Body: "budget:{{ worker_tool_budget_default }}"})
	testutil.FailErr(t, "render custom budget skill body", err)
	if want := "budget:7"; out != want {
		t.Fatalf("custom budget render = %q, want %q", out, want)
	}
}
