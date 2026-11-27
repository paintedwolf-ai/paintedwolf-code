package session_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/credentials/credentialstest"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/prompts"
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

	// Register real watch tools + bundled tool schemas on a toolhost runtime
	// so watch condition pruning runs through the production metadata path.
	configRoot := configlayout.FindModuleRoot()
	rt := newContextualToolsRuntime(t, configRoot)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(configRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load tool schemas", err)
	testutil.FailErr(t, "register watch tools", watch.RegisterWatchTools(rt.Registry, watch.ToolDeps{}))
	rt.Executor.Metadata.SetToolSchemas(schemas)
	manager.Coordinator.Guards.SetToolMetadata(rt.Executor.Metadata)

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
			_, err = manager.Submissions.Prompt(ctx, tc.sessionID, "check watch surface")
			testutil.FailErr(t, "Submissions.Prompt", err)
			if len(recorder.AllRequests()) <= beforeCalls {
				t.Fatalf("normal coordinator turn did not issue a provider request for %s", tc.name)
			}
			turnReq := recorder.LastRequest()
			turnKinds, turnStanding := extractWatchSchemaFacts(t, turnReq.Tools)

			for label, kinds := range map[string][]string{"preparation": prepKinds, "turn": turnKinds} {
				hasFileChange := slices.Contains(kinds, "file_change")
				hasGitRef := slices.Contains(kinds, "git_ref")
				if hasFileChange != tc.wantRootedKinds {
					t.Fatalf("%s (%s): file_change present=%v, want %v (kinds=%v)", tc.name, label, hasFileChange, tc.wantRootedKinds, kinds)
				}
				if hasGitRef != tc.wantRootedKinds {
					t.Fatalf("%s (%s): git_ref present=%v, want %v (kinds=%v)", tc.name, label, hasGitRef, tc.wantRootedKinds, kinds)
				}
				if !slices.Contains(kinds, "timer") {
					t.Fatalf("%s (%s): timer missing from watch conditions: %v", tc.name, label, kinds)
				}
			}
			if !slices.Equal(prepKinds, turnKinds) {
				t.Fatalf("%s: preparation watch kinds %v != turn watch kinds %v", tc.name, prepKinds, turnKinds)
			}
			if prepStanding != turnStanding {
				t.Fatalf("%s: standing offered prep=%v != turn=%v", tc.name, prepStanding, turnStanding)
			}
		})
	}
}
