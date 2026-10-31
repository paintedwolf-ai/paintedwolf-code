//go:build integration

package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		testutil.FailErr(t, "MkdirAll", err)
	}
	entries, err := os.ReadDir(src)
	testutil.FailErr(t, "ReadDir", err)
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			copyDir(t, srcPath, dstPath)
			continue
		}
		data, err := os.ReadFile(srcPath)
		testutil.FailErr(t, "ReadFile", err)
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			testutil.FailErr(t, "WriteFile", err)
		}
	}
}

func TestSessionWarmIncludesAgentsMDIndex(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	moduleRoot := configlayout.FindModuleRoot()
	fixture := filepath.Join(moduleRoot, "internal", "governance", "testdata", "agentsmd", "monorepo")
	projectDir := filepath.Join(t.TempDir(), "project")
	copyDir(t, fixture, projectDir)

	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "ok",
	}}}))
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: moduleRoot,
	}))

	// AGENTS.md follows the project trust switch.
	projectID := session.RegisterProjectContextForTest(t, mgr, projectDir)
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, projectID)
	testutil.FailErr(t, "create session", err)
	sess.WorkspacePath = projectDir
	mgr.Coordinator.PolicyIndex.Warm(t.Context(), sess)

	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "hello"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "continue"); err != nil {
		testutil.FailErr(t, "second Prompt", err)
	}
	reqs := rec.AllRequests()
	if len(reqs) == 0 {
		t.Fatal("expected LLM request")
	}
	var firstIndex string
	for i, req := range reqs {
		var index string
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "Available AGENTS.md files") {
				index = msg.Content
				if !msg.ContextPinned {
					t.Fatal("index is not pinned")
				}
			}
		}
		if index == "" {
			t.Fatalf("request %d lost index", i)
		}
		if i == 0 {
			firstIndex = index
		} else if index != firstIndex {
			t.Fatal("index changed across requests")
		}
	}
	combined := strings.Builder{}
	for _, msg := range reqs[0].Messages {
		if msg.Role == api.MessageRoleSystem {
			combined.WriteString(msg.Content)
			combined.WriteByte('\n')
		}
	}
	out := combined.String()
	if !strings.Contains(out, "Available AGENTS.md files") {
		t.Fatalf("missing AGENTS.md index inject:\n%s", out)
	}
	if !strings.Contains(out, "`lycaon/AGENTS.md`") {
		t.Fatalf("missing nested index path:\n%s", out)
	}
}

func TestManagerEffectiveLimitsUsesProjectOverlay(t *testing.T) {
	projectDir := t.TempDir()
	lycaonDir := filepath.Join(projectDir, settingsoverlay.DirName())
	if err := os.MkdirAll(lycaonDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	overlay := settings.SessionLimits{MaxIterations: 2}
	data, err := yaml.Marshal(overlay)
	testutil.FailErr(t, "yaml.Marshal failed", err)
	if err := os.WriteFile(filepath.Join(lycaonDir, "limits.yaml"), data, 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	limitsStore, err := settings.NewLimitsStoreAt(filepath.Join(t.TempDir(), "global-limits.yaml"))
	testutil.FailErr(t, "settings.NewLimitsStoreAt failed", err)

	mgr := session.NewHost(store.NewMemory(), session.Models{Client: llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}},
	}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.Limits.SetProvider(settings.ProjectLimitsAdapter{Store: limitsStore})
	mgr.Limits.SetMaxIterations(99)

	// Project limits require project settings trust.
	projectID := session.RegisterProjectContextForTest(t, mgr, projectDir)
	sess := &api.Session{ProjectID: projectID, WorkspacePath: projectDir}
	limits := mgr.Limits.Effective(t.Context(), sess)
	if limits.MaxIterations != 2 {
		t.Fatalf("effective max_iterations = %d want 2 from project overlay", limits.MaxIterations)
	}
}

func TestManagerEffectiveLimitsUsesHostWorkerMaxToolLoops(t *testing.T) {
	mgr := session.NewHost(store.NewMemory(), session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())

	child := &api.Session{WorkspacePath: t.TempDir(), AgentType: orchestration.ProfilePathExplorer, ParentSessionID: "parent-1"}
	limits := mgr.Limits.Effective(t.Context(), child)
	if limits.MaxIterations != spawn.DefaultWorkerMaxToolLoops {
		t.Fatalf("max_iterations = %d want default %d", limits.MaxIterations, spawn.DefaultWorkerMaxToolLoops)
	}

	child.MaxToolLoops = 5
	if lim := mgr.Limits.Effective(t.Context(), child); lim.MaxIterations != 5 {
		t.Fatalf("override max_iterations = %d want 5", lim.MaxIterations)
	}

	coord := &api.Session{WorkspacePath: child.WorkspacePath, AgentType: orchestration.ProfileCoordinator}
	if lim := mgr.Limits.Effective(t.Context(), coord); lim.MaxIterations != settings.DefaultSessionLimits().MaxIterations {
		t.Fatalf("coordinator max_iterations = %d want session default", lim.MaxIterations)
	}
}

func TestManagerEffectiveLimitsWithoutProviderUsesStaticCfg(t *testing.T) {
	cfg := settings.DefaultSessionLimits()
	cfg.MaxIterations = 7
	mgr := session.NewHost(store.NewMemory(), session.Models{Client: nil, Provider: nil, Limits: cfg, Cost: nil}, tools.NewStubRegistry())
	sess := &api.Session{WorkspacePath: t.TempDir()}
	limits := mgr.Limits.Effective(t.Context(), sess)
	if limits.MaxIterations != 7 {
		t.Fatalf("max_iterations = %d want 7", limits.MaxIterations)
	}
	_ = context.Background()
}
