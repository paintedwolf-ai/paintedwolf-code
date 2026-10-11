package session_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const incidentPlanReviewPolicyPrompt = `Please give feedback on this design review.

Remember everything is greenfield, we do not allow migration. Might be worth reviewing agents.md to see our standards.`

type failingKickPromptEngine struct{ err error }

func (e failingKickPromptEngine) Render(context.Context, string, map[string]any) (string, error) {
	return "", e.err
}

func (e failingKickPromptEngine) RenderInject(context.Context, string, map[string]any) (string, error) {
	return "", e.err
}

func (e failingKickPromptEngine) RenderGuidance(context.Context, string, map[string]any) (string, error) {
	return "", e.err
}

func (e failingKickPromptEngine) RenderKick(context.Context, string, map[string]any) (string, error) {
	return "", e.err
}

func (failingKickPromptEngine) Register(string, string) error { return nil }

func TestPromptDoesNotQueueGreenfieldBuildKickForPlanReviewPolicyPrompt(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	store := store.NewMemory()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewHost(store, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	mgr.SetProgressStore(progress.NewMemoryStore())
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())

	ctx := context.Background()
	sess, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "create coordinator session", err)
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, incidentPlanReviewPolicyPrompt); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	if id, ok := mgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID); ok && id != "" {
		t.Fatalf("TakePendingKickID = %q want empty (greenfield-build kick must not queue)", id)
	}
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleUser && strings.Contains(msg.Content, "Greenfield build — ship via plan") {
			t.Fatalf("greenfield-build kick must not prepend on policy prompt: %q", msg.Content)
		}
	}
}

func TestQueueCoordinatorKickPrependsOnPrompt(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	store := store.NewMemory()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewHost(store, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))

	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	mgr.Coordinator.Guidance.Emit(context.Background(), sess.ID, anchor.ComposeDone, anchor.Envelope{})
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "continue"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	req := rec.LastRequest()
	foundKick := false
	for _, msg := range req.Messages {
		if msg.Role == api.MessageRoleSystem && msg.Origin == api.MessageOriginHost && strings.Contains(msg.Content, "Compose succeeded") {
			foundKick = true
			break
		}
	}
	if !foundKick {
		t.Fatal("expected compose-done kick nudge as host system message")
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) < 2 {
		t.Fatalf("messages = %+v want kick + user rows", msgs)
	}
	var kickRow, userRow *api.Message
	for i := range msgs {
		if strings.Contains(msgs[i].Content, "Compose succeeded") {
			kickRow = &msgs[i]
		}
		if msgs[i].Content == "continue" {
			userRow = &msgs[i]
		}
	}
	if kickRow == nil || kickRow.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("kick visibility = %+v want internal", kickRow)
	}
	if userRow == nil || userRow.Visibility == api.MessageVisibilityInternal {
		t.Fatalf("user row = %+v want transcript-visible", userRow)
	}
}

func TestPromptRenderFailurePreservesCoordinatorKickForRetry(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	st := store.NewMemory()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewHost(st, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())
	mgr.SetPromptEngine(failingKickPromptEngine{err: errors.New("template unavailable")})

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.Coordinator.Guidance.Emit(ctx, sess.ID, anchor.ComposeDone, anchor.Envelope{})
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "continue"); err == nil {
		t.Fatal("Prompt error = nil, want kick render failure")
	}
	if id, ok := mgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID); !ok || !anchor.SameInform(id, anchor.ComposeDone) {
		t.Fatalf("pending kick after render failure = (%q, %v), want compose-done", id, ok)
	}

	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "continue"); err != nil {
		testutil.FailErr(t, "retry prompt", err)
	}
	if id, ok := mgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID); ok || id != "" {
		t.Fatalf("pending kick after successful retry = (%q, %v), want empty", id, ok)
	}
}
