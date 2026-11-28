package session_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/credentials/credentialstest"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/llmtest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
)

func extractWatchSchemaFacts(t *testing.T, metas []tools.ToolMeta) (kinds []string, hasStanding bool) {
	t.Helper()
	var watchMeta *tools.ToolMeta
	for i := range metas {
		if metas[i].Name == "watch" {
			watchMeta = &metas[i]
			break
		}
	}
	if watchMeta == nil {
		t.Fatalf("watch tool missing from offered tools: %v", toolNames(metas))
	}
	props, ok := watchMeta.ArgsSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("watch schema missing properties: %#v", watchMeta.ArgsSchema)
	}
	_, hasStanding = props["standing"]
	conds, _ := props["conditions"].(map[string]any)
	items, _ := conds["items"].(map[string]any)
	itemProps, _ := items["properties"].(map[string]any)
	kindField, _ := itemProps["kind"].(map[string]any)
	rawEnum, _ := kindField["enum"].([]any)
	kinds = make([]string, 0, len(rawEnum))
	for _, entry := range rawEnum {
		if s, ok := entry.(string); ok {
			kinds = append(kinds, s)
		}
	}
	return kinds, hasStanding
}

func TestWatchConditionSchemaMatchesRootAuthorityAcrossPreparationAndTurn(t *testing.T) {
	ctx := t.Context()
	recorder := llmtest.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}},
	}))
	fixture := setupContextualToolsFixtureWithLLM(t, api.SessionPostureBuild, recorder)
	manager := fixture.Mgr
	manager.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	board := &watchSchemaBoard{}
	manager.Coordinator.Runtime.SetBoardInject(board, board, nil)
	manager.Coordinator.Runtime.Board().SetInjectRenderer(nil)
	manager.Coordinator.Assembly.Workflow.Orientation = board

	// Register real watch tools + bundled tool schemas on a toolhost runtime
	// so watch condition pruning runs through the production metadata path.
	configRoot := configlayout.FindModuleRoot()
	rt := newContextualToolsRuntime(t, configRoot)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(configRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load tool schemas", err)
	testutil.FailErr(t, "register watch tools", watch.RegisterWatchTools(rt.Registry, watch.ToolDeps{}))
	rt.Executor.Metadata.SetToolSchemas(schemas)
	manager.Coordinator.Guards.SetToolMetadata(rt.Executor.Metadata)

	profileKinds := rt.Boundary.WatchConditions(orchestration.ProfileCoordinator)
	if len(profileKinds) == 0 {
		t.Fatal("coordinator profile has no watch conditions")
	}

	// Configure a local preparation-capable provider so BuildPreparation / ProjectPreparationRequest run.
	dir := t.TempDir()
	catalog, err := llm.NewProviderCatalogAt(filepath.Join(dir, "providers.local.yaml"))
	testutil.FailErr(t, "create preparation catalog", err)
	testutil.FailErr(t, "configure local preparation provider", catalog.Put(llm.ProviderEntry{ID: "preparation-test", Kind: "nativegen"}))
	testutil.FailErr(t, "reload preparation catalog", catalog.Reload())
	registry, err := llm.NewRegistry(ctx, catalog, credentialstest.Open(t, filepath.Join(dir, "credentials.age")), &nativegen.Availability{
		Initial: nativegen.Readiness{State: "ready", ContextTokens: 65536},
	})
	testutil.FailErr(t, "create preparation registry", err)
	registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	policy, err := llm.NewPolicyStoreAt(filepath.Join(dir, "model-policy.yaml"))
	testutil.FailErr(t, "create preparation policy", err)
	prepLLMService := &llm.Service{Registry: registry, Router: llm.NewStaticModelRouter(policy)}

	// Create a genuine zero-root project alongside the rooted fixture project.
	projects := project.NewSQLRegistry(fixture.Store.DB())
	rootlessProj, err := projecttest.CreateWithRoot(ctx, projects, t.TempDir())
	testutil.FailErr(t, "create rootless project seed", err)
	rootlessSess, err := fixture.Store.Sessions.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, rootlessProj.ID)
	testutil.FailErr(t, "create rootless session", err)
	_, err = projects.DetachRoot(ctx, rootlessProj.ID, rootlessProj.Roots[0].ID)
	testutil.FailErr(t, "detach last root from rootless project", err)
	testutil.FailErr(t, "set rootless agent type", manager.Chats.SetAgentType(ctx, rootlessSess.ID, orchestration.ProfileCoordinator))

	cases := []struct {
		name            string
		sessionID       string
		wantRootedKinds bool
	}{
		{
			name:            "rooted_session",
			sessionID:       fixture.Sess.ID,
			wantRootedKinds: true,
		},
		{
			name:            "rootless_session",
			sessionID:       rootlessSess.ID,
			wantRootedKinds: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testutil.FailErr(t, "select local model", fixture.Store.Sessions.UpdateSession(ctx, tc.sessionID, func(sess *api.Session) {
				sess.Status = api.SessionStatusIdle
				sess.ProviderID, sess.Model = "preparation-test", nativegen.DefaultModelID
				if tc.wantRootedKinds {
					sess.WorkspacePath = fixture.ProjectDir
				}
			}))

			// Entry point 1: Context preparation (BuildPreparation / ProjectPreparationRequest).
			manager.Coordinator.Model.LLMService = prepLLMService
			candidate, err := manager.Coordinator.BuildPreparation(ctx, tc.sessionID)
			testutil.FailErr(t, "BuildPreparation", err)
			if candidate.Key == "" || candidate.Run == nil {
				t.Fatalf("BuildPreparation produced empty candidate for %s", tc.name)
			}
			_, prepReq, ok, err := manager.Coordinator.ProjectPreparationRequest(ctx, tc.sessionID)
			testutil.FailErr(t, "ProjectPreparationRequest", err)
			if !ok {
				t.Fatalf("ProjectPreparationRequest returned ok=false for %s", tc.name)
			}
			prepKinds, prepStanding := extractWatchSchemaFacts(t, prepReq.Tools)

			// Entry point 2: Normal coordinator turn / next provider input.
			manager.Coordinator.Model.LLMService = nil
			beforeCalls := len(recorder.AllRequests())
			boardReadyBefore := board.oriented
			manager.Coordinator.Runtime.Board().InvalidateOrientation(tc.sessionID)
			_, err = manager.Submissions.Prompt(ctx, tc.sessionID, "check watch surface")
			testutil.FailErr(t, "Submissions.Prompt", err)
			if len(recorder.AllRequests()) <= beforeCalls {
				t.Fatalf("normal coordinator turn did not issue a provider request for %s", tc.name)
			}
			if tc.wantRootedKinds && board.oriented == boardReadyBefore {
				t.Fatal("rooted provider request did not exercise board orientation refresh")
			}
			turnReq := recorder.LastRequest()
			turnKinds, turnStanding := extractWatchSchemaFacts(t, turnReq.Tools)

			wantKinds := slices.Clone(profileKinds)
			if !tc.wantRootedKinds {
				wantKinds = slices.DeleteFunc(wantKinds, func(kind string) bool {
					return kind == "file_change" || kind == "git_ref"
				})
			}
			slices.Sort(wantKinds)
			for label, kinds := range map[string][]string{"preparation": prepKinds, "turn": turnKinds} {
				slices.Sort(kinds)
				if !slices.Equal(kinds, wantKinds) {
					t.Errorf("%s watch conditions = %v, want %v", label, kinds, wantKinds)
				}
			}
			if prepStanding != turnStanding {
				t.Fatalf("%s: standing offered prep=%v != turn=%v", tc.name, prepStanding, turnStanding)
			}
		})
	}
}

type watchSchemaBoard struct {
	oriented int
}

func (*watchSchemaBoard) BuildBoardSnapshot(context.Context, string, string, string, api.BoardDetailLevel, []projectroot.RootRef) (*api.BoardSnapshot, error) {
	return &api.BoardSnapshot{PackContentHash: "watch-authority"}, nil
}

func (*watchSchemaBoard) FormatBoardInject(api.BoardSnapshot, bool, time.Time) (string, bool) {
	return "Watch authority test board", true
}

func (b *watchSchemaBoard) RecordBoardOrientReady(context.Context, string, string) error {
	b.oriented++
	return nil
}

// The runtime profile catalog and the unpruned schema discover the subjects. The
// handler exercises registration-time authority independently of schema pruning.
func TestWatchConditionOfferMatchesRegistrationAcrossProfiles(t *testing.T) {
	ctx := t.Context()
	rt := newContextualToolsRuntime(t, configlayout.FindModuleRoot())
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load runtime profiles", err)
	if len(profiles) == 0 {
		t.Fatal("runtime has no tool profiles")
	}

	allowed := make(map[string]map[string]bool, len(profiles))
	for _, profile := range profiles {
		allowed[profile.ID] = make(map[string]bool, len(profile.WatchConditions))
		for _, kind := range profile.WatchConditions {
			allowed[profile.ID][kind] = true
		}
	}
	testutil.FailErr(t, "register watch", watch.RegisterWatchTools(rt.Registry, watch.ToolDeps{ProfileConditions: allowed}))
	registration, ok := rt.Registry.Definition("watch")
	if !ok {
		t.Fatal("watch registration missing")
	}
	vocabulary, _ := extractWatchSchemaFacts(t, rt.Registry.List())
	if len(vocabulary) == 0 {
		t.Fatal("watch schema declares no conditions")
	}
	for _, profile := range profiles {
		for _, kind := range profile.WatchConditions {
			if !slices.Contains(vocabulary, kind) {
				t.Errorf("profile %s allows %s, missing from unpruned schema", profile.ID, kind)
			}
		}
	}

	rootDir := t.TempDir()
	testutil.FailErr(t, "create Git marker", os.Mkdir(filepath.Join(rootDir, ".git"), 0o700))
	for _, profile := range profiles {
		for _, access := range []struct {
			name  string
			roots []projectroot.RootRef
			count int
		}{
			{"rooted", []projectroot.RootRef{{ID: "repo", Label: "repo", Path: rootDir, IsPrimary: true}}, 1},
			{"rootless", []projectroot.RootRef{{ID: "outside", Outside: true}}, 0},
		} {
			t.Run(profile.ID+"/"+access.name, func(t *testing.T) {
				filter := platform.ToolFilter{ProfileID: profile.ID, Rootless: access.count == 0, ProjectRootCount: access.count}
				offered, err := rt.Executor.Metadata.List(ctx, filter)
				testutil.FailErr(t, "list offered tools", err)
				if !profile.Tools["watch"] {
					if slices.Contains(toolNames(offered), "watch") {
						t.Error("watch offered to profile without watch permission")
					}
					return
				}
				kinds, _ := extractWatchSchemaFacts(t, offered)
				invocation := tools.ToolContext{
					Identity: tools.InvocationIdentity{Agent: profile.ID, ProjectID: "project", SessionID: "session"},
					Source:   tools.InvocationSource{Roots: access.roots},
					Local:    tools.InvocationLocal{LoopbackConnectGranted: true, LoopbackConnectPorts: []uint16{8080}},
				}
				allKinds := slices.Concat(vocabulary, kinds)
				slices.Sort(allKinds)
				for _, kind := range slices.Compact(allKinds) {
					args := map[string]any{"conditions": []any{watchConditionExample(kind)}}
					_, err := registration.Handler(ctx, args, invocation)
					if got, admitted := slices.Contains(kinds, kind), err == nil; got != admitted {
						t.Errorf("%s offered=%v, registration accepted=%v (error=%v)", kind, got, admitted, err)
					}
				}
			})
		}
	}
}

// Only kinds needing more than their discriminator require fixture values.
func watchConditionExample(kind string) map[string]any {
	fields := map[string]any{"kind": kind}
	switch kind {
	case "process_output":
		fields["handle"] = "cmd-example"
	case "http_ready":
		fields["url"] = "http://127.0.0.1:8080/ready"
	case "port_ready":
		fields["port"] = 8080
	case "file_change":
		fields["root"], fields["path"] = "repo", "status.txt"
	case "git_ref":
		fields["root"], fields["ref"] = "repo", "refs/heads/main"
	case "timer":
		fields["delay_ms"] = 1000
	}
	return fields
}
