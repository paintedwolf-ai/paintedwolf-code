package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPolicyAdvisoryReachesActualModelRequest(t *testing.T) {
	for _, anchor := range []string{oar.AnchorContentInput, oar.AnchorCoordinatorPreInvoke} {
		t.Run(anchor, func(t *testing.T) {
			t.Setenv("LYCAON_LLM_MOCK", "1")
			mem := store.NewMemory()
			root := t.TempDir()
			response := llm.MockResponseEntry{Pattern: ".", Text: "ok"}
			if anchor == oar.AnchorCoordinatorPreInvoke {
				response.ToolCalls = []llm.MockToolCall{{ID: "call-directory", Name: "list_dir", Args: map[string]any{"path": root}}}
				response.FollowUpText = "complete"
			}
			recorder := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{response}}))
			registry := tools.NewStubRegistry()
			testutil.FailErr(t, "register directory tool", registry.Register("list_dir", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				return "[]", nil
			}))
			mgr := session.NewHost(mem, session.Models{Client: recorder, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, registry)
			projects := project.NewMemoryRegistry()
			proj, err := project.CreateWithRoot(t.Context(), projects, root)
			testutil.FailErr(t, "create rooted project", err)
			mgr.SetProjectRegistry(projects)
			mgr.SetCoordinatorTurnFrameSource(&phaseStubCoordinator{phase: "expand", surface: "implement_investigate"})
			wirePromptTestManager(t, mgr)
			engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
			mgr.SetPromptEngine(engine)
			guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
			rule := &oar.Rule{OAR: "1.0", ID: "DELIVERY_PROBE", Kind: oar.KindPolicy, Anchor: anchor, Effect: oar.EffectNudge, OnError: "fail_closed", Enforcement: "enforce", Copy: oar.Copy{What: "Carry this boundary observation forward"}}
			pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, oar.NewCounterStore())
			pipeline.EnableAnchor(anchor)
			pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
			mgr.SetOARPipeline(pipeline, oar.NewRenderer(nil, nil))
			sess, err := mem.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, proj.ID)
			testutil.FailErr(t, "create session", err)
			_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "continue")
			testutil.FailErr(t, "run prompt", err)
			if anchor == oar.AnchorCoordinatorPreInvoke && len(recorder.AllRequests()) < 2 {
				t.Fatal("tool invocation did not reach a second model request")
			}
			found := false
			for _, message := range recorder.LastRequest().Messages {
				if strings.Contains(message.Content, "Code: DELIVERY_PROBE") {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s advisory absent from actual model request", anchor)
			}
			messages, err := mem.GetMessages(t.Context(), sess.ID)
			testutil.FailErr(t, "read transcript", err)
			found = false
			for _, message := range messages {
				if message.Kind == api.MessageKindCoordinatorGuidance && strings.Contains(message.Content, "Code: DELIVERY_PROBE") {
					found = true
					if len(message.ContentParts) != 2 || message.ContentParts[1].Authority != api.ContentAuthorityNone {
						t.Fatalf("feedback facts gained instruction authority: %#v", message.ContentParts)
					}
				}
			}
			if !found {
				t.Fatal("model feedback was not persisted")
			}
		})
	}
}
