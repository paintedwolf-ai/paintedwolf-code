package session_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestContextPreparationProjectsWithoutAdvancingChat(t *testing.T) {
	fixture := setupContextualToolsFixture(t, api.SessionPostureSpec)
	manager := fixture.Mgr
	manager.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	dir := t.TempDir()
	catalog, err := llm.NewProviderCatalogAt(filepath.Join(dir, "providers.local.yaml"))
	testutil.FailErr(t, "create preparation catalog", err)
	testutil.FailErr(t, "configure local preparation provider", catalog.Put(llm.ProviderEntry{ID: "preparation-test", Kind: "nativegen"}))
	testutil.FailErr(t, "reload preparation catalog", catalog.Reload())
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(dir, "credentials.age")), &nativegen.Availability{
		Initial: nativegen.Readiness{State: "ready", ContextTokens: 65536},
	})
	testutil.FailErr(t, "create preparation registry", err)
	registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	policy, err := llm.NewPolicyStoreAt(filepath.Join(dir, "model-policy.yaml"))
	testutil.FailErr(t, "create preparation policy", err)
	manager.Coordinator.Model.LLMService = &llm.Service{Registry: registry, Router: llm.NewStaticModelRouter(policy)}
	testutil.FailErr(t, "select local model", fixture.Store.UpdateSession(t.Context(), fixture.Sess.ID, func(sess *api.Session) {
		sess.Status = api.SessionStatusIdle
		sess.ProviderID, sess.Model = "preparation-test", nativegen.DefaultModelID
	}))
	before, err := fixture.Store.Get(t.Context(), fixture.Sess.ID)
	testutil.FailErr(t, "read initial chat", err)
	candidate, err := manager.Coordinator.BuildPreparation(t.Context(), fixture.Sess.ID)
	testutil.FailErr(t, "project preparation candidate", err)
	if candidate.Key == "" || candidate.Run == nil {
		t.Fatalf("local chat produced no preparation candidate: status=%s provider=%s model=%s history=%v eligible=%v frame=%T", before.Status, before.ProviderID, before.Model, manager.Coordinator.Model.History != nil, registry.CanPrepareContext(before.ProviderID), manager.Coordinator.Assembly.Frame)
	}
	if candidate.Run(t.Context()) {
		t.Fatal("preparation loaded an absent model runtime")
	}
	repeated, err := manager.Coordinator.BuildPreparation(t.Context(), fixture.Sess.ID)
	testutil.FailErr(t, "repeat candidate projection", err)
	if repeated.Key != candidate.Key {
		t.Fatal("unchanged host facts produced a different candidate")
	}
	after, err := fixture.Store.Get(t.Context(), fixture.Sess.ID)
	testutil.FailErr(t, "read projected chat", err)
	if before.CurrentTurn != after.CurrentTurn || before.Status != after.Status || before.CompactionGeneration != after.CompactionGeneration {
		t.Fatal("preparation changed authoritative chat state")
	}
	testutil.FailErr(t, "mark chat busy", fixture.Store.UpdateSession(t.Context(), fixture.Sess.ID, func(sess *api.Session) { sess.Status = api.SessionStatusBusy }))
	busy, err := manager.Coordinator.BuildPreparation(t.Context(), fixture.Sess.ID)
	testutil.FailErr(t, "skip busy chat", err)
	if busy.Run != nil {
		t.Fatal("busy chat admitted preparation")
	}
}
