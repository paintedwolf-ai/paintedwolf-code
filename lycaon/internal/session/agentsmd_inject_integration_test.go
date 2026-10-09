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
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
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
