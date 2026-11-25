package sessions

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
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
	coordinator := withRequestReader(t, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sessionID},
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "main", Path: gitRepoDir(t), IsPrimary: true}},
			ActiveRootID: "main",
		},
		Turn: tools.InvocationTurn{
			TurnSurfaceID:        "implement_routing",
			TurnOfferedToolNames: []string{"recall", "task", "web_search"},
		},
	})
	out, err := rt.Registry.Run(t.Context(), "skills_read", map[string]any{"need": "research-current-information"}, coordinator)
	testutil.FailErr(t, "read reachable stock skill", err)
	if !strings.Contains(out, "research-current-information") {
		t.Fatalf("coordinator surface read lost the stock skill:\n%s", out)
	}
	reachableBody := out

	// The skill needs web_search or fetch_url, so a reader offered only recall
	// gets SKILL_UNKNOWN or a discovery page, never the body.
	restricted := coordinator
	restricted.Identity.Agent = "mystery-agent"
	restricted.Turn.TurnSurfaceID = ""
	restricted.Turn.TurnOfferedToolNames = []string{"recall"}
	restricted = withRequestReader(t, restricted)
	out, err = rt.Registry.Run(t.Context(), "skills_read", map[string]any{"need": "research-current-information"}, restricted)
	if err != nil {
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "SKILL_UNKNOWN" {
			testutil.FailErr(t, "run skills_read for restricted reader", err)
		}
		out = ""
	}
	if strings.Contains(out, reachableBody) {
		t.Fatalf("restricted reader reached an unreachable skill:\n%s", out)
	}
}

// The wired skill template vars render the caller shape (coordinator, worker
// branch, git, root count) on coordinator surfaces and the host's spawn caps
// elsewhere.
func TestWiredSkillTemplateVarsRenderCallerShapeAndCaps(t *testing.T) {
	rt, sessionID := newWiredSkillHost(t)
	coordinator := withRequestReader(t, tools.ToolContext{
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
	})
	body := "coord:{% if is_coordinator %}yes{% else %}no{% endif %} branch:{% if on_worker_branch %}yes{% else %}no{% endif %} git:{% if has_git %}yes{% else %}no{% endif %} roots:{{ root_count }}"
	out, err := rt.Skills.RenderSkillBody(t.Context(), coordinator, skills.Skill{Name: "shape-probe", Body: body})
	testutil.FailErr(t, "render coordinator surface skill body", err)
	if want := "coord:yes branch:yes git:yes roots:2"; out != want {
		t.Fatalf("coordinator surface render = %q, want %q", out, want)
	}

	// An unregistered reader has no tool surface, so the vars fall back to the
	// spawn policy caps.
	unknown := withRequestReader(t, tools.ToolContext{Identity: tools.InvocationIdentity{Agent: "mystery-agent", SessionID: sessionID}})
	out, err = rt.Skills.RenderSkillBody(t.Context(), unknown, skills.Skill{Name: "caps-probe", Body: "in_flight:{{ max_in_flight }}"})
	testutil.FailErr(t, "render fallback skill body", err)
	if want := fmt.Sprintf("in_flight:%d", spawn.MaxInFlightTaskWorkers); out != want {
		t.Fatalf("fallback render = %q, want %q", out, want)
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
		return withRequestReader(t, tools.ToolContext{
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
		})
	}

	admittedTctx := makeCtx([]any{"text", "single_choice", "multi_choice", "secret"})
	narrowedEnumTctx := makeCtx([]any{"text", "single_choice", "multi_choice"})

	admittedReader := admittedTctx.Turn.Reader
	narrowedReader := narrowedEnumTctx.Turn.Reader
	if admittedReader.Facts()["can_ask_user_secret"] != true || narrowedReader.Facts()["can_ask_user_secret"] != false {
		t.Fatalf("can_ask_user_secret = admitted:%v narrowed:%v",
			admittedReader.Facts()["can_ask_user_secret"], narrowedReader.Facts()["can_ask_user_secret"])
	}
	if admittedReader.Fingerprint() == narrowedReader.Fingerprint() {
		t.Fatalf("Reader.Fingerprint() collided across response_type enum narrowing: %s", admittedReader.Fingerprint())
	}

	admittedOut, err := rt.Registry.Run(t.Context(), "skills_read", map[string]any{"need": "use-secrets-without-reading-them"}, admittedTctx)
	testutil.FailErr(t, "skills_read admitted", err)
	if !strings.Contains(admittedOut, "response_type: secret") {
		t.Fatalf("admitted render lost the secret response type:\n%s", admittedOut)
	}

	narrowedOut, err := rt.Registry.Run(t.Context(), "skills_read", map[string]any{"need": "use-secrets-without-reading-them"}, narrowedEnumTctx)
	testutil.FailErr(t, "skills_read narrowed enum", err)
	if strings.Contains(narrowedOut, "response_type: secret") {
		t.Fatalf("narrowed-enum render leaked response_type: secret:\n%s", narrowedOut)
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
				return withRequestReader(t, tools.ToolContext{
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
				})
			}
			makeAskDeliveryCtx := func(askSchema map[string]any) tools.ToolContext {
				return withRequestReader(t, tools.ToolContext{
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
				})
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
					if !strings.Contains(admOut, "deploy-to-a-hosting-platform") || !strings.Contains(admOut, "capability_request.host_resources") {
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
				if strings.Contains(bodyOut, "response_type: secret") != wide || strings.Contains(refOut, "secret_use") != wide {
					t.Fatalf("wide=%t delivery of the secret response type and secret_use runner field:\nbody:\n%s\nref:\n%s", wide, bodyOut, refOut)
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
			tctx := withRequestReader(t, tools.ToolContext{
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
			})
			reader := tctx.Turn.Reader
			vars, err := prompts.SkillVars(reader)
			testutil.FailErr(t, "SkillVars", err)
			if got := vars["can_ask_user_secret"]; got != tc.wantAllow {
				t.Fatalf("can_ask_user_secret = %v, want %t", got, tc.wantAllow)
			}
			out, err := rt.Registry.Run(t.Context(), "skills_read", map[string]any{"need": "use-secrets-without-reading-them"}, tctx)
			testutil.FailErr(t, "skills_read", err)
			if got := strings.Contains(out, "response_type: secret"); got != tc.wantAllow {
				t.Fatalf("response_type: secret delivered=%t, want %t:\n%s", got, tc.wantAllow, out)
			}
		})
	}
}

// withRequestReader carries the Reader of the request tctx's turn fields
// describe: its plan, or its surface's, with the offered names promoted.
func withRequestReader(t *testing.T, tctx tools.ToolContext) tools.ToolContext {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	profileID, err := prompts.ToolProfileForAgent(tctx.Identity.Agent)
	if err != nil {
		tctx.Turn.Reader = prompts.Reader{}
		return tctx
	}
	host := prompts.HostFacts{
		Profiles:       profiles,
		RootCount:      len(tctx.Source.Roots),
		WebSearch:      true,
		OnWorkerBranch: tctx.Source.SourceWorkspaceKind == api.SourceWorkspaceKindWorker && tctx.Source.WorkerBranchRoot != "",
	}
	if _, ok := gitrepo.Discover(tctx.ActiveRootPath()); ok && tctx.ActiveRootPath() != "" {
		host.HasGit, host.HasAnyGitRoot = true, true
	}
	plan := tctx.Turn.TurnToolPlan
	if !plan.Compiled() {
		if tctx.Turn.TurnSurfaceID != "" {
			plan, err = prompts.CompileSurfacePlan(tctx.Turn.TurnSurfaceID, max(host.RootCount, 1))
			testutil.FailErr(t, "compile surface plan", err)
		} else {
			plan = toolsurface.Compile(nil, nil)
		}
	}
	plan = plan.Promote(tctx.Turn.TurnOfferedToolNames...)
	reader, err := prompts.NewReader(host, prompts.TurnProjection{
		ProfileID:        profileID,
		SurfaceID:        tctx.Turn.TurnSurfaceID,
		Plan:             plan,
		Offered:          tctx.Turn.TurnOfferedToolNames,
		ProjectedSchemas: tctx.Turn.TurnOfferedToolSchemas,
	})
	testutil.FailErr(t, "request reader", err)
	tctx.Turn.Reader = reader
	return tctx
}
