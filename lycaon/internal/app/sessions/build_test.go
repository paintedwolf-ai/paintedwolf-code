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

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/webresearch"
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

func TestSkillToolReaderMapsReaderFacts(t *testing.T) {
	gitDir := gitRepoDir(t)
	plainDir := t.TempDir()
	for _, tc := range []struct {
		name          string
		tctx          tools.ToolContext
		wantProfileID string
		wantSurfaceID string
		wantOnBranch  bool
		wantHasGit    bool
		wantRootCount int
		wantErr       bool
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
			wantProfileID: "coordinator",
			wantSurfaceID: "implement_routing",
			wantOnBranch:  true,
			wantHasGit:    true,
			wantRootCount: 2,
		},
		{
			name: "unregistered agent returns error",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "mystery-agent"},
				Source: tools.InvocationSource{
					SourceWorkspaceKind: api.SourceWorkspaceKindWorker,
				},
			},
			wantErr: true,
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
			wantProfileID: "implement",
			wantHasGit:    true,
			wantRootCount: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := skillToolReader(t.Context(), nil, tc.tctx)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error for unregistered agent")
				}
				return
			}
			testutil.FailErr(t, "skillToolReader", err)
			facts := reader.Facts()
			onBranch, _ := facts["on_worker_branch"].(bool)
			hasGit, _ := facts["has_git"].(bool)
			if reader.ProfileID() != tc.wantProfileID || reader.SurfaceID() != tc.wantSurfaceID ||
				onBranch != tc.wantOnBranch || hasGit != tc.wantHasGit ||
				reader.RootCount() != tc.wantRootCount {
				t.Fatalf("skillToolReader = profile=%q surface=%q onBranch=%v hasGit=%v rootCount=%d",
					reader.ProfileID(), reader.SurfaceID(), onBranch, hasGit, reader.RootCount())
			}
		})
	}
}

func TestSkillReaderHasGitSplitsPrimaryFromSecondaryRoots(t *testing.T) {
	gitDir := gitRepoDir(t)
	plainDir := t.TempDir()
	for _, tc := range []struct {
		name        string
		tctx        tools.ToolContext
		wantPrimary bool
		wantAny     bool
	}{
		{
			name: "active root is a repository",
			tctx: tools.ToolContext{Source: tools.InvocationSource{
				Roots:        []projectroot.RootRef{{ID: "main", Path: gitDir, IsPrimary: true}},
				ActiveRootID: "main",
			}},
			wantPrimary: true,
			wantAny:     true,
		},
		{
			name: "worker branch root is a repository",
			tctx: tools.ToolContext{Source: tools.InvocationSource{
				Roots:            []projectroot.RootRef{{ID: "main", Path: plainDir, IsPrimary: true}},
				ActiveRootID:     "main",
				WorkerBranchRoot: gitDir,
			}},
			wantPrimary: true,
			wantAny:     true,
		},
		{
			name: "non-active attached root is a repository while primary is plain",
			tctx: tools.ToolContext{Source: tools.InvocationSource{
				Roots: []projectroot.RootRef{
					{ID: "main", Path: plainDir, IsPrimary: true},
					{Path: "   "},
					{ID: "lib", Path: gitDir},
				},
				ActiveRootID: "main",
			}},
			wantPrimary: false,
			wantAny:     true,
		},
		{
			name: "no repository anywhere",
			tctx: tools.ToolContext{Source: tools.InvocationSource{
				Roots:        []projectroot.RootRef{{ID: "main", Path: plainDir, IsPrimary: true}},
				ActiveRootID: "main",
			}},
			wantPrimary: false,
			wantAny:     false,
		},
		{
			name: "blank directories are skipped",
			tctx: tools.ToolContext{Source: tools.InvocationSource{
				WorkerBranchRoot: "   ",
			}},
			wantPrimary: false,
			wantAny:     false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := primaryRootHasGit(tc.tctx); got != tc.wantPrimary {
				t.Fatalf("primaryRootHasGit = %t, want %t", got, tc.wantPrimary)
			}
			if got := anyRootHasGit(tc.tctx); got != tc.wantAny {
				t.Fatalf("anyRootHasGit = %t, want %t", got, tc.wantAny)
			}
		})
	}
}

func TestSkillToolReaderHeldTools(t *testing.T) {
	plainRoot := []projectroot.RootRef{{ID: "main", Path: t.TempDir(), IsPrimary: true}}
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
			name: "coordinator surface keeps only offered tools in Held",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "coordinator"},
				Source:   tools.InvocationSource{Roots: plainRoot},
				Turn: tools.InvocationTurn{
					TurnSurfaceID:        "implement_routing",
					TurnOfferedToolNames: []string{"recall"},
				},
			},
			wantExact: map[string]bool{"recall": true},
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
			wantContains: []string{toolcontract.ReachCommandMutation},
		},
		{
			name: "worker profile scope allows command mutation",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "implementer"},
				Turn:     tools.InvocationTurn{TurnOfferedToolNames: []string{"command"}},
			},
			wantContains: []string{toolcontract.ReachCommandMutation},
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
				Source:   tools.InvocationSource{Roots: plainRoot},
				Turn: tools.InvocationTurn{
					TurnSurfaceID:        "implement_routing",
					TurnOfferedToolNames: []string{"command"},
				},
			},
			wantContains: []string{"command"},
			wantMissing:  []string{"recall", toolcontract.ReachCommandMutation},
		},
		{
			name: "unknown surface skips the surface merge",
			tctx: tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "coordinator"},
				Turn: tools.InvocationTurn{
					TurnSurfaceID:        "no_such_surface",
					TurnOfferedToolNames: []string{"command"},
				},
			},
			wantContains: []string{"command", toolcontract.ReachCommandMutation},
			wantMissing:  []string{"recall"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := skillToolReader(t.Context(), nil, tc.tctx)
			testutil.FailErr(t, "skillToolReader", err)
			offered := reader.Held()
			if tc.wantExact != nil && !maps.Equal(offered, tc.wantExact) {
				t.Fatalf("reader.Held() = %v, want exactly %v", offered, tc.wantExact)
			}
			for _, name := range tc.wantContains {
				if !offered[name] {
					t.Fatalf("reader.Held() is missing %q: %v", name, offered)
				}
			}
			for _, name := range tc.wantMissing {
				if offered[name] {
					t.Fatalf("reader.Held() unexpectedly holds %q: %v", name, offered)
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
func newWiredSkillHost(t *testing.T) (*toolhost.Runtime, string) {
	t.Helper()
	cat := extpackstest.StockCatalog(t)
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{
		ConfigRoot: configlayout.FindModuleRoot(),
		Catalog:    cat,
	})
	testutil.FailErr(t, "build native runtime", err)
	st := storetest.New(t, testdbseed.DefaultProjectID)
	mgr := session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	root := configlayout.FindModuleRoot()
	mgr.SetEffectiveCatalogDeps(root, cat, nil)
	mgr.Catalog.SetCatalogViewCache(catalogview.NewCache(root, slog.Default()))
	sess, err := st.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create probe session", err)
	wireSessionToolSources(mgr, rt, nil)
	return rt, sess.ID
}

// The wired skills catalog serves the stock pack skills a reader can reach:
// the coordinator surface reader reads a skill whose tools the surface
// addresses, while a reader holding only recall cannot reach it.
func TestWiredSkillsCatalogFiltersStockSkillsByReaderReach(t *testing.T) {
	rt, sessionID := newWiredSkillHost(t)
	coordinator := tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sessionID},
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "main", Path: gitRepoDir(t), IsPrimary: true}},
			ActiveRootID: "main",
		},
		Turn: tools.InvocationTurn{
			TurnSurfaceID:        "implement_routing",
			TurnOfferedToolNames: []string{"recall", "task", "web_search"},
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
// branch, git, root count) on coordinator surfaces and the host's spawn caps
// elsewhere.
func TestWiredSkillTemplateVarsRenderCallerShapeAndCaps(t *testing.T) {
	rt, sessionID := newWiredSkillHost(t)
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
			TurnOfferedToolNames: []string{"recall", "task", "web_search"},
		},
	}
	body := "coord:{% if is_coordinator %}yes{% else %}no{% endif %} branch:{% if on_worker_branch %}yes{% else %}no{% endif %} git:{% if has_git %}yes{% else %}no{% endif %} roots:{{ root_count }}"
	out, err := rt.Skills.RenderSkillBody(t.Context(), coordinator, skills.Skill{Name: "shape-probe", Body: body})
	testutil.FailErr(t, "render coordinator surface skill body", err)
	if want := "coord:yes branch:yes git:yes roots:2"; out != want {
		t.Fatalf("coordinator surface render = %q, want %q", out, want)
	}

	// An unregistered reader has no tool surface, so the vars fall back to the
	// spawn policy caps.
	unknown := tools.ToolContext{Identity: tools.InvocationIdentity{Agent: "mystery-agent", SessionID: sessionID}}
	out, err = rt.Skills.RenderSkillBody(t.Context(), unknown, skills.Skill{Name: "caps-probe", Body: "in_flight:{{ max_in_flight }}"})
	testutil.FailErr(t, "render fallback skill body", err)
	if want := fmt.Sprintf("in_flight:%d", spawn.MaxInFlightTaskWorkers); out != want {
		t.Fatalf("fallback render = %q, want %q", out, want)
	}
}

func TestSkillToolReaderDoesNotPromoteDeferredTools(t *testing.T) {
	gitDir := gitRepoDir(t)
	plainDir := t.TempDir()

	floorOnly, err := skillToolReader(t.Context(), nil, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator"},
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "main", Path: gitDir, IsPrimary: true}},
			ActiveRootID: "main",
		},
		Turn: tools.InvocationTurn{
			TurnSurfaceID:        "implement_investigate",
			TurnOfferedToolNames: []string{"command", "recall", "request_tools"},
		},
	})
	testutil.FailErr(t, "floorOnly skillToolReader", err)
	if slices.Contains(floorOnly.Offered(), "capture_page") || slices.Contains(floorOnly.Offered(), "fetch_url") {
		t.Fatalf("floor-only reader promoted deferred tools into Offered(): %v", floorOnly.Offered())
	}
	if !slices.Contains(floorOnly.Requestable(), "capture_page") || !slices.Contains(floorOnly.Requestable(), "fetch_url") {
		t.Fatalf("floor-only reader lost deferred tools from Requestable(): %v", floorOnly.Requestable())
	}

	zeroRoot, err := skillToolReader(t.Context(), nil, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator"},
		Turn: tools.InvocationTurn{
			TurnSurfaceID:        "implement_investigate",
			TurnOfferedToolNames: []string{"recall", "web_search"},
		},
	})
	testutil.FailErr(t, "zeroRoot skillToolReader", err)
	zeroFacts := zeroRoot.Facts()
	if zeroRoot.RootCount() != 0 || zeroFacts["has_workspace_roots"] != false || zeroFacts["has_file_tools"] != false {
		t.Fatalf("0-root facts = root_count:%d has_workspace_roots:%v has_file_tools:%v",
			zeroRoot.RootCount(), zeroFacts["has_workspace_roots"], zeroFacts["has_file_tools"])
	}
	if slices.Contains(zeroRoot.Offered(), "command") || slices.Contains(zeroRoot.Requestable(), "command") {
		t.Fatalf("0-root reader exposed command: offered=%v requestable=%v", zeroRoot.Offered(), zeroRoot.Requestable())
	}

	cat := extpackstest.StockCatalog(t)
	st := storetest.New(t, testdbseed.DefaultProjectID)
	mgr := session.NewHost(st, session.Models{Limits: settings.DefaultSessionLimits()}, nil)
	postures, err := profiles.LoadPostureRegistry()
	testutil.FailErr(t, "load posture registry", err)
	mgr.Profiles.SetPostureRegistry(postures)
	root := configlayout.FindModuleRoot()
	mgr.SetEffectiveCatalogDeps(root, cat, nil)
	mgr.Catalog.SetCatalogViewCache(catalogview.NewCache(root, slog.Default()))
	sess, err := st.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	webCfg, err := webresearch.OpenConfigStore(filepath.Join(t.TempDir(), "webresearch.yaml"))
	testutil.FailErr(t, "open web config store", err)
	searchDisabled := false
	testutil.FailErr(t, "disable web search", webCfg.ApplyPrefs(nil, nil, &searchDisabled, nil))
	mgr.Coordinator.Context.WebResearch = webCfg
	externalHidden := false
	mgr.Profiles.SetCommandExternalHidden(func() bool { return externalHidden })

	hostTctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sess.ID},
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "main", Path: gitDir, IsPrimary: true}},
			ActiveRootID: "main",
		},
		Turn: tools.InvocationTurn{
			TurnSurfaceID:        "implement_investigate",
			TurnOfferedToolNames: []string{"command", "recall", "web_search"},
		},
	}
	rShown, err := skillToolReader(t.Context(), mgr, hostTctx)
	testutil.FailErr(t, "rShown skillToolReader", err)
	if rShown.WebSearch() || slices.Contains(rShown.Offered(), "web_search") {
		t.Fatalf("WebSearch=false still offered web_search: webSearch=%t offered=%v", rShown.WebSearch(), rShown.Offered())
	}
	if !rShown.CanReachProgram("external") || !rShown.CanReachProgram("private-service") {
		t.Fatalf("externalHidden=false: external=%t private-service=%t", rShown.CanReachProgram("external"), rShown.CanReachProgram("private-service"))
	}
	externalHidden = true
	rHidden, err := skillToolReader(t.Context(), mgr, hostTctx)
	testutil.FailErr(t, "rHidden skillToolReader", err)
	if rHidden.CanReachProgram("external") || !rHidden.CanReachProgram("private-service") {
		t.Fatalf("externalHidden=true: external=%t private-service=%t", rHidden.CanReachProgram("external"), rHidden.CanReachProgram("private-service"))
	}

	nonGitPrimary, err := skillToolReader(t.Context(), nil, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator"},
		Source: tools.InvocationSource{
			Roots: []projectroot.RootRef{
				{ID: "r1", Path: plainDir, IsPrimary: true},
				{ID: "r2", Path: gitDir},
			},
			ActiveRootID: "r1",
		},
		Turn: tools.InvocationTurn{
			TurnSurfaceID:        "implement_investigate",
			TurnOfferedToolNames: []string{"command", "recall"},
		},
	})
	testutil.FailErr(t, "nonGitPrimary skillToolReader", err)
	f1 := nonGitPrimary.Facts()
	if f1["has_git"] != false || f1["has_any_git_root"] != true || f1["can_git_read"] != false {
		t.Fatalf("(primary=non-git, secondary=git) facts: has_git=%v has_any_git_root=%v can_git_read=%v",
			f1["has_git"], f1["has_any_git_root"], f1["can_git_read"])
	}

	gitPrimary, err := skillToolReader(t.Context(), nil, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator"},
		Source: tools.InvocationSource{
			Roots: []projectroot.RootRef{
				{ID: "r1", Path: gitDir, IsPrimary: true},
				{ID: "r2", Path: plainDir},
			},
			ActiveRootID: "r1",
		},
		Turn: tools.InvocationTurn{
			TurnSurfaceID:        "implement_investigate",
			TurnOfferedToolNames: []string{"command", "recall"},
		},
	})
	testutil.FailErr(t, "gitPrimary skillToolReader", err)
	f2 := gitPrimary.Facts()
	if f2["has_git"] != true || f2["has_any_git_root"] != true || f2["can_git_read"] != true {
		t.Fatalf("(primary=git, secondary=non-git) facts: has_git=%v has_any_git_root=%v can_git_read=%v",
			f2["has_git"], f2["has_any_git_root"], f2["can_git_read"])
	}
}

// Both projections keep the secret object; only response_type.enum narrows in the second.
func TestSkillReadUseSecretsDistinguishesResponseTypeEnumNarrowing(t *testing.T) {
	rt, sessionID := newWiredSkillHost(t)
	makeAskSchema := func(responseEnum []any) map[string]any {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt":        map[string]any{"type": "string"},
				"response_type": map[string]any{"type": "string", "enum": responseEnum},
				"secret": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":  map[string]any{"type": "string"},
						"scope": map[string]any{"type": "string", "enum": []any{"chat", "project"}},
					},
				},
			},
		}
	}
	offered := []string{"ask_user", "command", "secret_generate", "secret_list", "secret_revoke"}
	makeCtx := func(enumVals []any) tools.ToolContext {
		return tools.ToolContext{
			Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sessionID},
			Source: tools.InvocationSource{
				Roots:        []projectroot.RootRef{{ID: "main", Path: gitRepoDir(t), IsPrimary: true}},
				ActiveRootID: "main",
			},
			Turn: tools.InvocationTurn{
				TurnSurfaceID:        "implement_investigate",
				TurnOfferedToolNames: offered,
				TurnOfferedToolSchemas: map[string]map[string]any{
					"ask_user":        makeAskSchema(enumVals),
					"command":         {"type": "object"},
					"secret_generate": {"type": "object"},
					"secret_list":     {"type": "object"},
					"secret_revoke":   {"type": "object"},
				},
			},
		}
	}

	admittedTctx := makeCtx([]any{"text", "single_choice", "multi_choice", "secret"})
	narrowedEnumTctx := makeCtx([]any{"text", "single_choice", "multi_choice"})

	admittedReader, err := skillToolReader(t.Context(), nil, admittedTctx)
	testutil.FailErr(t, "admitted skillToolReader", err)
	narrowedReader, err := skillToolReader(t.Context(), nil, narrowedEnumTctx)
	testutil.FailErr(t, "narrowed skillToolReader", err)
	if admittedReader.Facts()["can_ask_user_secret"] != true || narrowedReader.Facts()["can_ask_user_secret"] != false {
		t.Fatalf("can_ask_user_secret = admitted:%v narrowed:%v",
			admittedReader.Facts()["can_ask_user_secret"], narrowedReader.Facts()["can_ask_user_secret"])
	}
	if admittedReader.Fingerprint() == narrowedReader.Fingerprint() {
		t.Fatalf("Reader.Fingerprint() collided across response_type enum narrowing: %s", admittedReader.Fingerprint())
	}

	admittedOut, err := rt.Registry.Run(t.Context(), "skills_read", map[string]any{"need": "use-secrets-without-reading-them"}, admittedTctx)
	testutil.FailErr(t, "skills_read admitted", err)
	if !strings.Contains(admittedOut, "call `ask_user` with `response_type: secret` and a `secret` object") || strings.Contains(admittedOut, "report the need upstream") {
		t.Fatalf("admitted render lost ask_user secret call:\n%s", admittedOut)
	}

	narrowedOut, err := rt.Registry.Run(t.Context(), "skills_read", map[string]any{"need": "use-secrets-without-reading-them"}, narrowedEnumTctx)
	testutil.FailErr(t, "skills_read narrowed enum", err)
	if strings.Contains(narrowedOut, "response_type: secret") || !strings.Contains(narrowedOut, "— report the need upstream") {
		t.Fatalf("narrowed-enum render leaked response_type: secret or lost upstream handoff:\n%s", narrowedOut)
	}
}

// Exercises wired admission and delivery across wide->narrow and narrow->wide with identical offered names.
func TestWiredSkillAdmissionAndDeliveryDistinguishProjectedSchemasBothOrders(t *testing.T) {
	t.Setenv("LYCAON_TEST_VERCEL_CLI", "1")
	configtest.Overlay(t, map[config.Rel]string{
		config.HostResources: `version: 1
resources:
  - id: vercel-cli
    family: cloud
    label: Vercel CLI
    category: Cloud
    description: Vercel deployment CLI.
    surfaces: [process_exec]
    realizations:
      - discover:
          environment:
            names: [LYCAON_TEST_VERCEL_CLI]
        connections:
          - mode: proxy
`,
	})
	wideAsk := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt":        map[string]any{"type": "string"},
			"response_type": map[string]any{"type": "string", "enum": []any{"text", "single_choice", "multi_choice", "secret"}},
			"secret":        map[string]any{"type": "object"},
		},
	}
	narrowAsk := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt":        map[string]any{"type": "string"},
			"response_type": map[string]any{"type": "string", "enum": []any{"text", "single_choice", "multi_choice"}},
			"secret":        map[string]any{"type": "object"},
		},
	}
	wideVerify := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"secret_use": map[string]any{"type": "array"},
			"capability_request": map[string]any{
				"type":       "object",
				"properties": map[string]any{"host_resources": map[string]any{"type": "array"}},
			},
		},
	}
	narrowVerify := map[string]any{
		"type":       "object",
		"properties": map[string]any{"socks_proxy": map[string]any{"type": "boolean"}},
	}

	for _, order := range []struct {
		name      string
		wideFirst bool
	}{
		{"wide_then_narrow", true},
		{"narrow_then_wide", false},
	} {
		t.Run(order.name, func(t *testing.T) {
			cat := extpackstest.StockCatalog(t)
			root := configlayout.FindModuleRoot()
			rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: root, Catalog: cat})
			testutil.FailErr(t, "build runtime", err)
			st := storetest.New(t, testdbseed.DefaultProjectID)
			mgr := session.NewHost(st, session.Models{Limits: settings.DefaultSessionLimits()}, nil)
			mgr.SetEffectiveCatalogDeps(root, cat, nil)
			mgr.Catalog.SetCatalogViewCache(catalogview.NewCache(root, slog.Default()))
			hr, err := hostresources.NewService(t.TempDir())
			testutil.FailErr(t, "new host resources", err)
			mgr.Profiles.SetHostResources(hr)
			sess, err := st.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			wireSessionToolSources(mgr, rt, hr)

			repoDir := gitRepoDir(t)
			makeRunnerCtx := func(verifySchema map[string]any) tools.ToolContext {
				return tools.ToolContext{
					Identity: tools.InvocationIdentity{Agent: "implementer", SessionID: sess.ID},
					Source: tools.InvocationSource{
						Roots:        []projectroot.RootRef{{ID: "main", Path: repoDir, IsPrimary: true}},
						ActiveRootID: "main",
					},
					Turn: tools.InvocationTurn{
						TurnOfferedToolNames: []string{"command", "secret_generate", "secret_list", "secret_revoke", "verify"},
						TurnOfferedToolSchemas: map[string]map[string]any{
							"command":         {"type": "object"},
							"secret_generate": {"type": "object"},
							"secret_list":     {"type": "object"},
							"secret_revoke":   {"type": "object"},
							"verify":          verifySchema,
						},
					},
				}
			}
			makeAskDeliveryCtx := func(askSchema map[string]any) tools.ToolContext {
				return tools.ToolContext{
					Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sess.ID},
					Source: tools.InvocationSource{
						Roots:        []projectroot.RootRef{{ID: "main", Path: repoDir, IsPrimary: true}},
						ActiveRootID: "main",
					},
					Turn: tools.InvocationTurn{
						TurnSurfaceID:        "implement_investigate",
						TurnOfferedToolNames: []string{"ask_user", "command", "secret_generate", "secret_list", "secret_revoke"},
						TurnOfferedToolSchemas: map[string]map[string]any{
							"ask_user":        askSchema,
							"command":         {"type": "object"},
							"secret_generate": {"type": "object"},
							"secret_list":     {"type": "object"},
							"secret_revoke":   {"type": "object"},
						},
					},
				}
			}
			checkStep := func(wide bool) {
				runnerCtx := makeRunnerCtx(narrowVerify)
				askCtx := makeAskDeliveryCtx(narrowAsk)
				if wide {
					runnerCtx = makeRunnerCtx(wideVerify)
					askCtx = makeAskDeliveryCtx(wideAsk)
				}
				admOut, admErr := rt.Registry.Run(t.Context(), "skills_read", map[string]any{"need": "deploy-to-a-hosting-platform"}, runnerCtx)
				if wide {
					testutil.FailErr(t, "admit deploy-to-a-hosting-platform on wide verify", admErr)
					if !strings.Contains(admOut, "Skill: deploy-to-a-hosting-platform") || !strings.Contains(admOut, "capability_request.host_resources") {
						t.Fatalf("wide admission missing deploy-to-a-hosting-platform body:\n%s", admOut)
					}
				} else {
					if admErr != nil {
						var reject *toolrejection.ToolReject
						if !errors.As(admErr, &reject) || reject.Code != "SKILL_UNKNOWN" {
							testutil.FailErr(t, "skills_read narrow admission", admErr)
						}
						admOut = ""
					}
					if strings.Contains(admOut, "deploy-to-a-hosting-platform") {
						t.Fatalf("narrow verify without host_resources falsely admitted deploy-to-a-hosting-platform:\n%s", admOut)
					}
				}

				bodyOut, err := rt.Registry.Run(t.Context(), "skills_read", map[string]any{"need": "use-secrets-without-reading-them"}, askCtx)
				testutil.FailErr(t, "skills_read body", err)
				refOut, err := rt.Registry.Run(t.Context(), "skills_read", map[string]any{
					"need":     "use-secrets-without-reading-them",
					"resource": "references/managed-references.md",
				}, runnerCtx)
				testutil.FailErr(t, "skills_read resource", err)
				if wide {
					if !strings.Contains(bodyOut, "response_type: secret") || !strings.Contains(refOut, "When `verify` sets up an authenticated service") {
						t.Fatalf("wide delivery lost secret collection or verify runner:\nbody:\n%s\nref:\n%s", bodyOut, refOut)
					}
				} else {
					if strings.Contains(bodyOut, "response_type: secret") || !strings.Contains(bodyOut, "— report the need upstream") || strings.Contains(refOut, "## Repeated service use") {
						t.Fatalf("narrow delivery leaked response_type: secret or verify runner:\nbody:\n%s\nref:\n%s", bodyOut, refOut)
					}
				}
			}
			checkStep(order.wideFirst)
			checkStep(!order.wideFirst)
		})
	}
}

// Positive and negative field-level and root-allOf ask_user projections through can_ask_user_secret.
func TestCanAskUserSecretRespectsFieldAndRootAllOfIntersections(t *testing.T) {
	rt, sessionID := newWiredSkillHost(t)
	repoDir := gitRepoDir(t)
	offered := []string{"ask_user", "command", "secret_generate", "secret_list", "secret_revoke"}

	for _, tc := range []struct {
		name      string
		askSchema map[string]any
		wantAllow bool
	}{
		{
			name: "field_level_negative_enum_narrowed_by_allOf_const",
			askSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"response_type": map[string]any{
						"type":  "string",
						"enum":  []any{"text", "secret"},
						"allOf": []any{map[string]any{"const": "text"}},
					},
					"secret": map[string]any{"type": "object"},
				},
			},
			wantAllow: false,
		},
		{
			name: "field_level_positive_enum_and_allOf_both_admit_secret",
			askSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"response_type": map[string]any{
						"type":  "string",
						"enum":  []any{"text", "secret"},
						"allOf": []any{map[string]any{"enum": []any{"single_choice", "secret"}}},
					},
					"secret": map[string]any{"type": "object"},
				},
			},
			wantAllow: true,
		},
		{
			name: "root_allOf_negative_branch_narrows_response_type_to_text",
			askSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"response_type": map[string]any{"type": "string", "enum": []any{"text", "secret"}},
					"secret":        map[string]any{"type": "object"},
				},
				"allOf": []any{
					map[string]any{
						"properties": map[string]any{
							"response_type": map[string]any{"const": "text"},
						},
					},
				},
			},
			wantAllow: false,
		},
		{
			name: "root_allOf_positive_branches_preserve_secret",
			askSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"response_type": map[string]any{"type": "string", "enum": []any{"text", "single_choice", "secret"}},
					"secret":        map[string]any{"type": "object"},
				},
				"allOf": []any{
					map[string]any{
						"properties": map[string]any{
							"response_type": map[string]any{"enum": []any{"text", "secret"}},
						},
					},
					map[string]any{"required": []any{"response_type"}},
				},
			},
			wantAllow: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tctx := tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sessionID},
				Source: tools.InvocationSource{
					Roots:        []projectroot.RootRef{{ID: "main", Path: repoDir, IsPrimary: true}},
					ActiveRootID: "main",
				},
				Turn: tools.InvocationTurn{
					TurnSurfaceID:        "implement_investigate",
					TurnOfferedToolNames: offered,
					TurnOfferedToolSchemas: map[string]map[string]any{
						"ask_user":        tc.askSchema,
						"command":         {"type": "object"},
						"secret_generate": {"type": "object"},
						"secret_list":     {"type": "object"},
						"secret_revoke":   {"type": "object"},
					},
				},
			}
			reader, err := skillToolReader(t.Context(), nil, tctx)
			testutil.FailErr(t, "skillToolReader", err)
			vars, err := prompts.SkillVars(reader)
			testutil.FailErr(t, "SkillVars", err)
			if got := vars["can_ask_user_secret"]; got != tc.wantAllow {
				t.Fatalf("can_ask_user_secret = %v, want %t", got, tc.wantAllow)
			}
			out, err := rt.Registry.Run(t.Context(), "skills_read", map[string]any{"need": "use-secrets-without-reading-them"}, tctx)
			testutil.FailErr(t, "skills_read", err)
			if tc.wantAllow && !strings.Contains(out, "response_type: secret") {
				t.Fatalf("positive witness omitted response_type: secret:\n%s", out)
			}
			if !tc.wantAllow && (strings.Contains(out, "response_type: secret") || !strings.Contains(out, "— report the need upstream")) {
				t.Fatalf("negative witness leaked response_type: secret:\n%s", out)
			}
		})
	}
}
