package session

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func testMockConfig(t *testing.T) *llm.MockConfig {
	t.Helper()
	cfg, err := llm.LoadMockConfig()
	if err != nil {
		t.Fatalf("load mock config: %v", err)
	}
	return cfg
}

func newTestManager(t *testing.T) (*Manager, *store.Memory) {
	t.Helper()
	store := store.NewMemory()
	registry := tools.NewStubRegistry()
	mgr := NewManager(store, llm.NewMockProvider(testMockConfig(t)), registry, settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.SetToolInvoker(testtool.RegistryInvoker{Registry: registry})
	// Rewind checkpoints use a state root separate from the project.
	mgr.SetDataDir(t.TempDir())
	return mgr, store
}

func newRootedTestManager(t *testing.T) (*Manager, *store.Memory, string) {
	t.Helper()
	mgr, store := newTestManager(t)
	return mgr, store, attachTestProject(t, mgr)
}

func attachTestProject(t *testing.T, mgr *Manager) string {
	t.Helper()
	projects := project.NewMemoryRegistry()
	created, err := projects.Create(context.Background(), project.CreateParams{Roots: []project.AttachRootParams{{
		Path: t.TempDir(),
	}}})
	testutil.FailErr(t, "create test project", err)
	mgr.SetProjectRegistry(projects)
	return created.ID
}

func TestPromptTextResponse(t *testing.T) {
	mgr, store := newTestManager(t)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	resp, err := mgr.Prompt(ctx, sess.ID, "hello")
	testutil.FailErr(t, "mgr.Prompt failed", err)
	if resp.MessageID == "" {
		t.Fatal("expected message id")
	}

	msgs, err := mgr.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "mgr.GetMessages failed", err)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	if msgs[1].Role != api.MessageRoleAssistant {
		t.Fatalf("last role = %q", msgs[1].Role)
	}
}

func TestPromptWithToolCall(t *testing.T) {
	mgr, store, projectID := newRootedTestManager(t)
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load closeout hints", err)
	mgr.SetWorkflowHints(hints, nil)
	rejectFmt := guidance.NewStaticRejectFormatter(hints)
	mgr.SetRejectFormatter(rejectFmt)
	pipeline := testCoordinatorPreInvokePipeline(t)
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	mgr.SetOARPipeline(pipeline, oar.NewRenderer(rejectFmt, nil))
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, projectID)
	testutil.FailErr(t, "create session in store", err)

	_, err = mgr.Prompt(ctx, sess.ID, "read the readme")
	testutil.FailErr(t, "mgr.Prompt failed", err)

	msgs, err := mgr.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "mgr.GetMessages failed", err)
	if len(msgs) < 4 {
		t.Fatalf("messages = %d, want tool exchange and closeout", len(msgs))
	}
	if msgs[1].Role != api.MessageRoleAssistant {
		t.Fatalf("expected assistant message, got %+v", msgs[1])
	}
	if msgs[2].Role != api.MessageRoleTool {
		t.Fatalf("expected tool message, got %q", msgs[2].Role)
	}
	var final api.Message
	var reports int
	for _, message := range msgs {
		if message.Kind == api.MessageKindCompletionReport {
			final = message
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("completion reports = %d, want 1", reports)
	}
	if final.Role != api.MessageRoleAssistant || len(final.ToolCalls) != 0 {
		t.Fatalf("expected prose-only final assistant on its own row, got %+v", final)
	}
	if final.Grounding == nil || !final.Grounding.HostAssembled || len(final.Grounding.CitedEvidence) != 0 {
		t.Fatalf("uncited fixture should finish without invented citations: %+v", final.Grounding)
	}
}

func TestMaxIterationCap(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	mgr, store, projectID := newRootedTestManager(t)
	if hintCfg, err := guidance.LoadHintConfig(extpacks.Bundled(hintregistry.DefaultDir)); err == nil {
		mgr.SetWorkflowHints(hintCfg, nil)
	}
	mgr.SetMaxIterations(3)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, projectID)
	testutil.FailErr(t, "create session in store", err)

	_, err = mgr.Prompt(ctx, sess.ID, "infinite loop")
	var empty *failure.ProviderEmptyCompletionError
	if !errors.As(err, &empty) {
		t.Fatalf("tool-only closeout error = %v, want empty completion", err)
	}

	msgs, err := mgr.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "mgr.GetMessages failed", err)
	// The repeating fixture refuses the final prose request; no blank answer commits.
	want := 7
	if len(msgs) != want {
		t.Fatalf("messages = %d, want %d", len(msgs), want)
	}
	if msgs[5].Role != api.MessageRoleUser || msgs[5].Visibility != api.MessageVisibilityInternal {
		t.Fatalf("msg[5] = %+v, want internal closeout nudge", msgs[5])
	}
	if msgs[6].Kind != api.MessageKindDraft || msgs[6].DraftStatus != api.DraftStatusWithdrawn {
		t.Fatalf("failed final draft = %+v, want withdrawn draft", msgs[6])
	}
	settled, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "read failed closeout session", err)
	if settled.Status != api.SessionStatusIdle {
		t.Fatalf("failed closeout status = %q, want idle", settled.Status)
	}
}

func TestSessionHistoryAccumulation(t *testing.T) {
	mgr, store := newTestManager(t)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	for i := 0; i < 3; i++ {
		if _, err := mgr.Prompt(ctx, sess.ID, "hello"); err != nil {
			testutil.FailErr(t, "mgr.Prompt failed", err)
		}
	}

	msgs, err := mgr.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "mgr.GetMessages failed", err)
	if len(msgs) != 6 {
		t.Fatalf("messages = %d, want 6", len(msgs))
	}
	ids := map[string]struct{}{}
	for _, m := range msgs {
		if _, ok := ids[m.ID]; ok {
			t.Fatalf("duplicate message id %q", m.ID)
		}
		ids[m.ID] = struct{}{}
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].CreatedAt.Before(msgs[i-1].CreatedAt) {
			t.Fatal("timestamps not monotonic")
		}
	}
}

func TestToolCallError(t *testing.T) {
	store := store.NewMemory()
	reg := tools.NewStubRegistry()
	reg.SetFail("read", fmt.Errorf("read failed"))
	mgr := NewManager(store, llm.NewMockProvider(testMockConfig(t)), reg, settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.SetToolInvoker(testtool.RegistryInvoker{Registry: reg})
	projectID := attachTestProject(t, mgr)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, projectID)
	testutil.FailErr(t, "create session in store", err)

	if _, err := mgr.Prompt(ctx, sess.ID, "read the readme"); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
	}

	msgs, err := mgr.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "mgr.GetMessages failed", err)
	if len(msgs) < 3 {
		t.Fatalf("messages = %d", len(msgs))
	}
	if msgs[2].Role != api.MessageRoleTool {
		t.Fatalf("role = %q", msgs[2].Role)
	}
	if msgs[2].Content == "" {
		t.Fatal("expected tool error content")
	}
}

func TestConcurrentPrompts(t *testing.T) {
	mgr, store := newTestManager(t)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	done := make(chan error, 2)
	go func() { _, err := mgr.Prompt(ctx, sess.ID, "hello one"); done <- err }()
	go func() { _, err := mgr.Prompt(ctx, sess.ID, "hello two"); done <- err }()
	if err := <-done; err != nil {
		testutil.FailErr(t, "operation failed", err)
	}
	if err := <-done; err != nil {
		testutil.FailErr(t, "operation failed", err)
	}

	msgs, err := mgr.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "mgr.GetMessages failed", err)
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4", len(msgs))
	}
}
