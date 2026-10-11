//go:build integration

package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type feedbackReadFailureStore struct {
	Store
	failRead bool
}

func (s *feedbackReadFailureStore) GetMessage(ctx context.Context, sessionID, messageID string) (api.Message, error) {
	if s.failRead {
		s.failRead = false
		return api.Message{}, errors.New("injected feedback read failure")
	}
	return s.Store.GetMessage(ctx, sessionID, messageID)
}

func TestPolicyFeedbackRetriesRenderingAndPersistenceWithoutLoss(t *testing.T) {
	mem := store.NewMemory()
	storage := &feedbackReadFailureStore{Store: mem}
	mgr := NewHost(storage, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	decision := &oar.Decision{Effect: oar.EffectNudge, Advisories: []oar.Advisory{{Code: "PROBE", Rule: "example/PROBE", Copy: map[string]string{"what": "frozen observation"}}}}
	testutil.FailErr(t, "queue before renderer", mgr.Coordinator.Guidance.DeliverAdvisories(t.Context(), sess.ID, oar.AnchorCoordinatorPostTurn, decision))
	lease := mgr.Coordinator.Runtime.Kicks().LeasePolicyFeedback(sess.ID)
	if _, err := mgr.Coordinator.Guidance.TakePolicy(t.Context(), sess.ID); err == nil {
		t.Fatal("missing renderer accepted")
	}
	mgr.SetOARPipeline(nil, advisoryTestManager(t).Coordinator.Feedback.Renderer())
	storage.failRead = true
	if _, err := mgr.Coordinator.Guidance.TakePolicy(t.Context(), sess.ID); err == nil {
		t.Fatal("injected read failure not returned")
	}
	retry := mgr.Coordinator.Runtime.Kicks().LeasePolicyFeedback(sess.ID)
	if retry.ID != lease.ID || len(retry.Entries) != 1 {
		t.Fatal("failure lost or replaced staged feedback")
	}
	messages, err := mgr.Coordinator.Guidance.TakePolicy(t.Context(), sess.ID)
	testutil.FailErr(t, "retry persisted feedback", err)
	stored, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read transcript", err)
	if len(messages) != 1 || len(stored) != 1 || messages[0].ID != lease.ID {
		t.Fatalf("retry duplicated feedback: returned=%d stored=%d", len(messages), len(stored))
	}
}

func TestPolicyAdvisoryRoutingAtEveryHostBoundary(t *testing.T) {
	anchors := []string{oar.AnchorToolPreInvoke, oar.AnchorCoordinatorPreInvoke, oar.AnchorSessionPreInvoke, oar.AnchorToolHandler, oar.AnchorToolRejected, oar.AnchorCoordinatorPostTurn, oar.AnchorCoordinatorCloseoutCheck, oar.AnchorWorkerReportCheck, oar.AnchorWorkerFinalize, oar.AnchorToolPost, oar.AnchorCredentialAssignment, oar.AnchorContentInput, oar.AnchorContentOutput, oar.AnchorContentToolResult}
	for _, anchor := range anchors {
		for _, effect := range []oar.Effect{oar.EffectWarn, oar.EffectNudge} {
			t.Run(anchor+"/"+string(effect), func(t *testing.T) {
				mgr := advisoryTestManager(t)
				rule := &oar.Rule{OAR: "1.0", ID: "PROBE", Kind: oar.KindPolicy, Anchor: anchor, Effect: effect, Enforcement: "enforce", OnError: "fail_closed", Copy: oar.Copy{What: "Frozen boundary observation"}}
				pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, oar.NewCounterStore())
				pipeline.EnableAnchor(anchor)
				mgr.SetOARPipeline(pipeline, mgr.Coordinator.Feedback.Renderer())
				gc := oar.NewGuardContext()
				gc.Session.SessionID = "session"
				res, err := pipeline.EvaluateBlock(t.Context(), anchor, gc)
				testutil.FailErr(t, "evaluate boundary", err)
				if res.Decision == nil || res.Decision.Effect != effect {
					t.Fatalf("decision = %#v", res.Decision)
				}
				inline := anchor == oar.AnchorWorkerFinalize || effect == oar.EffectWarn && (anchor == oar.AnchorToolPost || anchor == oar.AnchorCredentialAssignment)
				queued := mgr.Coordinator.Runtime.Kicks().LeasePolicyFeedback("session")
				if (len(queued.Entries) == 0) != inline {
					t.Fatalf("inline=%v queued=%d", inline, len(queued.Entries))
				}
				if inline {
					feedback, blocked, err := mgr.Coordinator.Feedback.RenderResult(t.Context(), anchor, res)
					testutil.FailErr(t, "render inline advisory", err)
					if blocked || feedback == nil || !strings.Contains(feedback.Body, "Frozen boundary observation") {
						t.Fatalf("inline feedback=%#v blocked=%v", feedback, blocked)
					}
				}
			})
		}
	}
}

func TestPolicyAdvisoryPersistenceUsesScreenedBytesAndAcknowledgesOnce(t *testing.T) {
	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	renderer := advisoryTestManager(t).Coordinator.Feedback.Renderer()
	mgr.SetOARPipeline(nil, renderer)
	mgr.Runner.Transcript.SetRedactor(func(_ context.Context, msg api.Message) (api.Message, bool) {
		msg.Content = strings.ReplaceAll(msg.Content, "private-fixture", "screened-fixture")
		for n := range msg.ContentParts {
			msg.ContentParts[n].Content = strings.ReplaceAll(msg.ContentParts[n].Content, "private-fixture", "screened-fixture")
		}
		return msg, true
	})
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	for _, id := range []string{"first", "second"} {
		decision := &oar.Decision{Effect: oar.EffectNudge, Advisories: []oar.Advisory{{Code: "PROBE", Rule: "example/PROBE", Copy: map[string]string{"what": "private-fixture"}, Data: map[string]any{"subject": map[string]any{"kind": "task", "id": id}, "observation": "private-fixture"}}}}
		testutil.FailErr(t, "queue advisory", mgr.Coordinator.Guidance.DeliverAdvisories(t.Context(), sess.ID, oar.AnchorCoordinatorCloseoutCheck, decision))
	}
	messages, err := mgr.Coordinator.Guidance.TakePolicy(t.Context(), sess.ID)
	testutil.FailErr(t, "take feedback", err)
	if len(messages) != 1 || strings.Contains(messages[0].Content, "private-fixture") || !strings.Contains(messages[0].Content, "second") {
		t.Fatalf("delivery lost screening or subjects: %#v", messages)
	}
	stored, err := mem.GetMessage(t.Context(), sess.ID, messages[0].ID)
	testutil.FailErr(t, "read stored feedback", err)
	if stored.Content != messages[0].Content {
		t.Fatal("model received different bytes from history")
	}
	again, err := mgr.Coordinator.Guidance.TakePolicy(t.Context(), sess.ID)
	testutil.FailErr(t, "take acknowledged feedback", err)
	if len(again) != 0 {
		t.Fatal("acknowledged feedback delivered twice")
	}
}
