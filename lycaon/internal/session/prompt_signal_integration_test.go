//go:build integration

package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestGateBlockedSingleImperativeChannel(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	hintCfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	mgr.SetWorkflowHints(hintCfg, nil)
	mgr.SetCoordinatorTurnFrameSource(&phaseStubCoordinator{
		phase:  "implement",
		brief:  "brief",
		failed: []string{"evidence_passed:verify"},
	})
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())

	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureSpec}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	mgr.Coordinator.Guidance.Emit(context.Background(), sess.ID, anchor.GateBlocked, anchor.Envelope{})
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "why blocked"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && strings.Contains(msg.Content, "WORKFLOW_GATE_UNMET") {
			t.Fatal("gate-blocked kick should suppress WORKFLOW_GATE_UNMET hint")
		}
	}
	var foundKick bool
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role != api.MessageRoleSystem || msg.Origin != api.MessageOriginHost {
			continue
		}
		if strings.Contains(strings.ToLower(msg.Content), "gate") {
			foundKick = true
			break
		}
	}
	if !foundKick {
		t.Fatalf("expected gate-blocked host message in prompt assembly: %+v", rec.LastRequest().Messages)
	}
}
