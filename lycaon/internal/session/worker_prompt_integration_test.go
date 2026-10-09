//go:build integration

package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubWorkerContext struct {
	ctx inject.WorkerLegContext
}

func (s *stubWorkerContext) BuildWorkerPromptContext(_ string, _ *api.Session) (inject.WorkerLegContext, error) {
	return s.ctx, nil
}

func setupWorkerPromptFixture(t *testing.T) (*session.Host, *store.Memory, *llm.RecordingClient) {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "1")
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "ok",
	}}}))
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	agents := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(context.Background(), agents); err != nil {
		testutil.FailErr(t, "LoadRequiredAgentRegistry", err)
	}
	mgr.Profiles.SetAgentRegistry(agents)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	mgr.SetBoardInject(&board.InjectBuilder{SnapshotBuilder: &board.SnapshotBuilder{
		Repo: repotest.NewProvider(t),
		Git:  git.NewManager(),
	}}, board.DefaultInjectFormatter())
	return mgr, store, rec
}

func wireSkillsCatalogForPromptTest(t *testing.T, mgr *session.Host) {
	t.Helper()
	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "trust-surfaces.yaml"))
	testutil.FailErr(t, "trust surfaces", err)
	mgr.SetEffectiveCatalogDeps(configlayout.FindModuleRoot(), extpacks.Active(), surfaces)
}

func TestChildPromptIncludesWorkerContextBlock(t *testing.T) {
	mgr, store, rec := setupWorkerPromptFixture(t)
	mgr.SetWorkerContextBuilder(&stubWorkerContext{ctx: inject.WorkerLegContext{
		LegID:              "leg-implement-1",
		WorkflowID:         "default-pipeline",
		PhaseID:            "implement",
		TopologyPattern:    "pipeline",
		CompletionCriteria: []string{"job:complete"},
		LegTools:           []string{"read", "write"},
		Checklist:          []string{implementVerifyChecklist},
	}})

	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module worker.board\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	parent, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create failed", err)
	testdbseed.BindSessionWorkspace(t, store, parent.ID, dir)
	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "implement feature",
	})
	testutil.FailErr(t, "mgr.Workers.SpawnChild failed", err)
	if _, err := mgr.Submissions.Prompt(ctx, child.ID, "implement feature"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	var legBlock string
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role != api.MessageRoleSystem {
			continue
		}
		if strings.Contains(msg.Content, inject.WorkerLegInjectSentinel) {
			legBlock = msg.Content
		}
	}
	if legBlock == "" {
		t.Fatalf("messages = %+v", rec.LastRequest().Messages)
	}
	if !strings.Contains(legBlock, "leg-implement-1") || !strings.Contains(legBlock, "## Leg tools") {
		t.Fatalf("leg block = %q", legBlock)
	}
	if !strings.Contains(legBlock, "lycaon-worker-leg:v1") {
		t.Fatalf("leg block missing inject sentinel: %q", legBlock)
	}
	if !strings.Contains(legBlock, "phase_id: implement") || !strings.Contains(legBlock, "- read") {
		t.Fatalf("leg block missing phase or tools: %q", legBlock)
	}
}

func TestChildPromptIncludesPlaybookWhenMatched(t *testing.T) {
	mgr, _, rec := setupWorkerPromptFixture(t)
	mgr.SetWorkerContextBuilder(&stubWorkerContext{ctx: inject.WorkerLegContext{
		Checklist: []string{implementVerifyChecklist, "Branch on Code: from tool rejects"},
	}})

	ctx := context.Background()
	parent, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create failed", err)
	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "go",
	})
	testutil.FailErr(t, "mgr.Workers.SpawnChild failed", err)
	if _, err := mgr.Submissions.Prompt(ctx, child.ID, "go"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	found := false
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && strings.Contains(msg.Content, implementVerifyChecklist) {
			found = true
		}
	}
	if !found {
		t.Fatal("playbook line missing from worker context")
	}
}

func TestChildPromptDistinctAgentTypes(t *testing.T) {
	mgr, _, rec := setupWorkerPromptFixture(t)
	mgr.SetWorkerContextBuilder(&stubWorkerContext{ctx: inject.WorkerLegContext{Checklist: []string{"x"}}})

	ctx := context.Background()
	parent, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create failed", err)
	implChild, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "implement",
	})
	testutil.FailErr(t, "mgr.Workers.SpawnChild failed", err)
	if _, err := mgr.Submissions.Prompt(ctx, implChild.ID, "implement"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	var implL1 string
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && !strings.Contains(msg.Content, inject.WorkerLegInjectSentinel) {
			implL1 = msg.Content
			break
		}
	}

	reviewerChild, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: "plan-reviewer",
		Prompt:    "review plan",
	})
	testutil.FailErr(t, "mgr.Workers.SpawnChild failed", err)
	if _, err := mgr.Submissions.Prompt(ctx, reviewerChild.ID, "review plan"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	var reviewL1 string
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && !strings.Contains(msg.Content, inject.WorkerLegInjectSentinel) {
			reviewL1 = msg.Content
			break
		}
	}
	if implL1 == "" || reviewL1 == "" {
		t.Fatal("expected L1 system prompts for both workers")
	}
	if implL1 == reviewL1 {
		t.Fatal("implementer and plan-reviewer L1 personas must differ")
	}
	if !strings.Contains(implL1, "implement product code") && !strings.Contains(implL1, "Implementer") {
		t.Fatalf("implementer L1 = %q", implL1)
	}
	if !strings.Contains(reviewL1, "advisory") || !strings.Contains(reviewL1, "Plan Reviewer") {
		t.Fatalf("plan-reviewer L1 = %q", reviewL1)
	}
}

func TestCoordinatorPromptUnchanged(t *testing.T) {
	mgr, _, rec := setupWorkerPromptFixture(t)
	mgr.SetWorkerContextBuilder(&stubWorkerContext{ctx: inject.WorkerLegContext{
		LegID: "should-not-appear",
	}})

	ctx := context.Background()
	sess, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create failed", err)
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "coordinate"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	for _, msg := range rec.LastRequest().Messages {
		if strings.Contains(msg.Content, inject.WorkerLegInjectSentinel) {
			t.Fatalf("coordinator got worker block: %q", msg.Content)
		}
	}
}

func TestWorkerPromptKeepsCuratedSkillsOutOfStandingPrompt(t *testing.T) {
	mgr, _, rec := setupWorkerPromptFixture(t)
	wireSkillsCatalogForPromptTest(t, mgr)
	mgr.SetWorkerContextBuilder(&stubWorkerContext{ctx: inject.WorkerLegContext{Checklist: []string{"survey"}}})

	ctx := context.Background()
	parent, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create", err)
	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileRepoResearcher,
		Prompt:    "trace the relevant code",
	})
	testutil.FailErr(t, "mgr.Workers.SpawnChild", err)
	curated := map[string]bool{}
	for _, sk := range mgr.Profiles.CompileMachine(ctx, child, orchestration.ProfileRepoResearcher).Surface.Skills {
		curated[sk.Name] = true
	}
	for _, name := range []string{"investigate-code-history", "trace-a-system-invariant"} {
		if !curated[name] {
			t.Fatalf("repo researcher missing curated skill %q", name)
		}
	}
	for _, name := range []string{"verify-a-change", "commit-in-logical-groups"} {
		if curated[name] {
			t.Fatalf("repo researcher received skill outside its agent definition: %q", name)
		}
	}
	if _, err := mgr.Submissions.Prompt(ctx, child.ID, "trace the relevant code"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt", err)
	}

	var system string
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem {
			system += "\n" + msg.Content
		}
	}
	for name := range curated {
		if strings.Contains(system, "- "+name) {
			t.Fatalf("standing prompt listed skill %q", name)
		}
	}
}

func TestCoordinatorInvestigateIndexesAllSkills(t *testing.T) {
	mgr, _, _ := setupWorkerPromptFixture(t)
	wireSkillsCatalogForPromptTest(t, mgr)
	ctx := context.Background()
	sess, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create", err)
	listed := map[string]bool{}
	for _, sk := range mgr.Profiles.CompileMachine(ctx, sess, prompts.CoordinatorProfileID).Surface.Skills {
		listed[sk.Name] = true
	}
	for _, name := range []string{
		"commit-in-logical-groups",
		"diagnose-a-tls-failure",
		"orchestrate-a-large-task",
		"reach-a-network-service",
		"trace-a-system-invariant",
		"use-secrets-without-reading-them",
	} {
		if !listed[name] {
			t.Fatalf("coordinator roster missing %q", name)
		}
	}
	// No host-resource service is wired, so a skill gated on docker or podman stays unadvertised.
	if listed["run-the-project-stack"] {
		t.Fatal("coordinator roster advertised a skill whose host resources are unmet")
	}
}
