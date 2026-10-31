//go:build integration

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/oar"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOverCapTurnMarksTheCheckpointTruncated(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()
	testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(dir, "src"), 0o755))

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "touch everything")
	testutil.FailErr(t, "prompt", err)
	anchor := visibleUserMessageIDs(t, mgr, sessionID)[0]

	for i := 0; i < sessioncheckpoint.MaxPaths+20; i++ {
		rel := "src/f" + strconv.Itoa(i) + ".go"
		testutil.FailErr(t, "seed "+rel,
			os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte("before"), 0o644))
		mgr.Chats.Captures.RecordPrimaryMutation(ctx, sessionID, rel)
	}

	man, err := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store)).Load(t.Context(), sessionID, anchor)
	testutil.FailErr(t, "load manifest", err)
	if len(man.Paths) != sessioncheckpoint.MaxPaths {
		t.Fatalf("captured %d paths, want the cap %d", len(man.Paths), sessioncheckpoint.MaxPaths)
	}
	if !man.Truncated {
		t.Fatal("an over-cap turn must leave a checkpoint that reports itself truncated")
	}
}

func TestManagerPostureOverlayIntegration(t *testing.T) {
	reg, err := profiles.LoadPostureRegistry()
	testutil.FailErr(t, "profiles.LoadPostureRegistry failed", err)
	dir := t.TempDir()
	overlayDir := filepath.Join(dir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	overlay := []byte(`
postures:
  vet:
    label: Audit
`)
	if err := os.WriteFile(filepath.Join(overlayDir, "postures.yaml"), overlay, 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	mgr := NewHost(store.NewMemory(), Models{Client: llm.NewMockProvider(testMockConfig(t)), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.Profiles.SetPostureRegistry(reg)
	if err := mgr.Profiles.WarmPostureOverlay(dir); err != nil {
		testutil.FailErr(t, "mgr.Profiles.WarmPostureOverlay failed", err)
	}

	// Project postures require project settings trust.
	projectID := RegisterProjectContextForTest(t, mgr, dir)
	sess := &api.Session{ID: "overlay", ProjectID: projectID, Posture: api.SessionPostureVet, WorkspacePath: dir}

	effective, err := mgr.Profiles.EffectivePostures(t.Context(), sess)
	testutil.FailErr(t, "mgr.Profiles.EffectivePostures failed", err)
	spec, err := effective.Get(api.SessionPostureVet)
	testutil.FailErr(t, "effective.Get failed", err)
	if spec.Label != "Audit" {
		t.Fatalf("effective label = %q", spec.Label)
	}
}

func TestWarmPostureOverlayRejectsUnknownPosture(t *testing.T) {
	reg, err := profiles.LoadPostureRegistry()
	testutil.FailErr(t, "profiles.LoadPostureRegistry failed", err)
	dir := t.TempDir()
	overlayDir := filepath.Join(dir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "postures.yaml"), []byte(`
postures:
  plan:
    label: Plan
`), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.Profiles.SetPostureRegistry(reg)
	if err := mgr.Profiles.WarmPostureOverlay(dir); err == nil {
		t.Fatal("expected unknown overlay posture error")
	}
}

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
