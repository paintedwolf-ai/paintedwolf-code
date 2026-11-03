package session

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRecoverOrphanedTurnsClearsStuckBusy(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	hub := events.NewMemoryHub()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetEventPublisher(&events.Publisher{Hub: hub})
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	testutil.FailErr(t, "busy", mem.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "Subscribe", err)
	t.Cleanup(unsub)

	testutil.FailErr(t, "RecoverOrphanedTurns", mgr.Stops.Recovery.RecoverOrphanedTurns(ctx))
	got, err := mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "Get", err)
	if got.Status != api.SessionStatusIdle {
		t.Fatalf("status = %s want idle", got.Status)
	}

	// Still busy at boot is a host teardown, not a turn failure.
	if got := awaitIdleDisposition(t, ch); got != api.SessionIdleDispositionInterrupted {
		t.Fatalf("recovered disposition = %q, want interrupted", got)
	}
}

func awaitIdleDisposition(t *testing.T, ch <-chan api.EventEnvelope) api.SessionIdleDisposition {
	t.Helper()
	for {
		select {
		case envelope, ok := <-ch:
			if !ok {
				t.Fatal("event stream closed before a session idle event")
			}
			raw, err := json.Marshal(envelope.Data)
			testutil.FailErr(t, "marshal envelope data", err)
			var ev api.SessionEvent
			if err := json.Unmarshal(raw, &ev); err != nil {
				continue
			}
			if ev.Status != api.SessionStatusIdle {
				continue
			}
			return ev.IdleDisposition
		case <-t.Context().Done():
			t.Fatal("no session idle event published")
			return ""
		}
	}
}

func TestRecoverInterruptedToolResultsAppendsMissingToolRows(t *testing.T) {
	ctx := context.Background()
	database := testdbfixture.Open(t, "recover-turns.db")

	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sqlStore := store.NewSQL(database)
	mgr := NewHost(sqlStore, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	recorder := invocation.NewSQLRecorder(database)
	mgr.SetInvocationRecorder(recorder)

	sess, err := sqlStore.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	testutil.FailErr(t, "busy", sqlStore.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract is not declared")
	}
	testutil.FailErr(t, "append draft", sqlStore.AppendMessages(ctx, sess.ID, api.Message{
		ID: "asst-1", Role: api.MessageRoleAssistant, Kind: api.MessageKindDraft,
		Content:   "working",
		ToolCalls: []api.ToolCall{{ID: "call-cmd", Name: "command"}},
	}))
	_, err = recorder.Begin(ctx, invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: sess.ID,
		AssistantMessageID: "asst-1", ToolCallID: "call-cmd", ToolName: "command",
		Args: map[string]any{"command": "sleep 600"}, Contract: contract,
	})
	testutil.FailErr(t, "begin receipt", err)

	if _, err := recorder.InterruptRunning(ctx); err != nil {
		testutil.FailErr(t, "interrupt", err)
	}
	testutil.FailErr(t, "RecoverOrphanedTurns", mgr.Stops.Recovery.RecoverOrphanedTurns(ctx))
	testutil.FailErr(t, "RecoverInterruptedToolResults", mgr.Stops.Recovery.RecoverInterruptedToolResults(ctx))
	testutil.FailErr(t, "RecoverInterruptedToolResults replay", mgr.Stops.Recovery.RecoverInterruptedToolResults(ctx))

	got, err := sqlStore.Get(ctx, sess.ID)
	testutil.FailErr(t, "Get", err)
	if got.Status != api.SessionStatusIdle {
		t.Fatalf("status = %s want idle", got.Status)
	}
	msgs, err := sqlStore.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	found := 0
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if msg.ToolResult.ToolCallID == "call-cmd" && msg.ToolResult.Outcome == api.ToolResultOutcomeError {
			found++
			if msg.ToolResult.AssistantMessageID != "asst-1" {
				t.Fatalf("interrupted result lost its assistant identity: %+v", msg.ToolResult)
			}
			if msg.ToolResult.Invocation == nil || msg.ToolResult.Invocation.Status != api.InvocationStatusInterrupted {
				t.Fatalf("interrupted result lost its settled receipt: %+v", msg.ToolResult)
			}
		}
	}
	if found != 1 {
		t.Fatalf("interrupted tool-result count = %d want 1, messages=%+v", found, msgs)
	}
}

type reviewCheckpointRecorder struct {
	sourceledger.Recorder
	inputs []sourceledger.StructuralCheckpointInput
}

func (r *reviewCheckpointRecorder) CreateStructuralCheckpoint(
	_ context.Context,
	in sourceledger.StructuralCheckpointInput,
) (sourceledger.Checkpoint, error) {
	r.inputs = append(r.inputs, in)
	return sourceledger.Checkpoint{
		ProjectID: in.ProjectID, Kind: in.Kind, SessionID: in.SessionID, Turn: in.Turn,
	}, nil
}

func TestReviewCheckpointOnlyOpensForRealUserIntent(t *testing.T) {
	ctx := t.Context()
	sessions := store.NewMemory()
	mgr := NewHost(sessions, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	recorder := &reviewCheckpointRecorder{}
	mgr.SetSourceLedger(recorder, tools.SourceHistory{}, nil, nil, recorder, nil)
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	message := func(id string, origin api.MessageOrigin, visibility api.MessageVisibility, kind api.MessageKind) api.Message {
		return api.Message{
			ID: id, Role: api.MessageRoleUser, Content: id, Origin: origin,
			Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted,
			Visibility: visibility, Kind: kind, CreatedAt: time.Now().UTC(),
		}
	}
	first := message("user-1", api.MessageOriginUser, api.MessageVisibilityTranscript, "")
	mgr.Runner.Instructions.ReviewCheckpoint(ctx, sess.ID, first)
	testutil.FailErr(t, "append first user turn", sessions.AppendMessages(ctx, sess.ID, first))

	hostKick := message("host-kick", api.MessageOriginHost, api.MessageVisibilityInternal, api.MessageKindHostKick)
	mgr.Runner.Instructions.ReviewCheckpoint(ctx, sess.ID, hostKick)
	testutil.FailErr(t, "append host kick", sessions.AppendMessages(ctx, sess.ID, hostKick))

	second := message("user-2", api.MessageOriginUser, api.MessageVisibilityTranscript, "")
	mgr.Runner.Instructions.ReviewCheckpoint(ctx, sess.ID, second)
	if len(recorder.inputs) != 2 {
		t.Fatalf("review checkpoints = %d, want one for each of two user turns", len(recorder.inputs))
	}
	if recorder.inputs[0].Turn != 1 || recorder.inputs[1].Turn != 2 {
		t.Fatalf("review checkpoint turns = %d, %d, want 1, 2",
			recorder.inputs[0].Turn, recorder.inputs[1].Turn)
	}
}

func (r *reviewCheckpointRecorder) TurnCheckpoint(context.Context, string, string, int) (sourceledger.Checkpoint, bool, error) {
	return sourceledger.Checkpoint{}, false, nil
}

func TestReviewGuidanceFollowsLiveVerdictGate(t *testing.T) {
	for _, id := range []anchor.ID{anchor.ReviewLoopContinue, anchor.ReviewLoopDecide} {
		for _, pending := range []bool{false, true} {
			for _, staged := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/pending=%v/staged=%v", id, pending, staged), func(t *testing.T) {
					mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
					mgr.SetWorkflowDomains(workflowDomainFixture(verdictPendingView{pending: true}))
					kicks := mgr.Coordinator.Runtime.Kicks()
					review := anchor.InformRender(id)
					other := anchor.InformRender(anchor.ComposeDone)
					kicks.QueueDeferred("review-session", review)
					kicks.QueueDeferred("review-session", other)
					kicks.QueueDeferred("other-session", review)
					if staged {
						if got := kicks.TakePendingKickID("review-session"); got != review {
							t.Fatalf("open review guidance = %q, want %q", got, review)
						}
					}
					mgr.SetWorkflowDomains(workflowDomainFixture(verdictPendingView{pending: pending}))
					want := other
					if pending {
						want = review
					}
					if got := mgr.Coordinator.Guidance.PendingIDs(t.Context(), "review-session"); len(got) == 0 || got[0] != want {
						t.Fatalf("guidance = %v, want %q first for current verdict gate", got, want)
					}
					if got, ok := kicks.PeekPendingKickID("other-session"); !ok || got != review {
						t.Fatalf("another session's guidance changed: %q, %v", got, ok)
					}
				})
			}
		}
	}
}

func TestListenPlanOffersStandardRungs(t *testing.T) {
	t.Parallel()
	broker := &ListenCheckpointBroker{}
	card, err := broker.buildListenCard(tools.LocalListenAsk{
		Command: "docker compose up", ToolName: "command", ProjectDir: "/tmp/proj",
		Ports: []uint16{3000},
	}, "child", "root", []uint16{3000})
	if err != nil {
		t.Fatalf("buildListenCard: %v", err)
	}
	assertStandardPortRungs(t, card.Plan, hitl.ApprovalSubjectLocalListen)
}

func TestLoopbackPlanOffersStandardRungs(t *testing.T) {
	t.Parallel()
	broker := &LoopbackCheckpointBroker{}
	card, err := broker.buildCard(tools.LoopbackConnectAsk{
		Command: "curl localhost:8123", ToolName: "command", ProjectDir: "/tmp/proj",
		Ports: []uint16{8123},
	}, "root", "root", []uint16{8123})
	testutil.FailErr(t, "buildCard", err)
	assertStandardPortRungs(t, card.Plan, hitl.ApprovalSubjectLoopbackConnect)
}

func assertStandardPortRungs(t *testing.T, plan *hitl.ApprovalPlan, kind hitl.ApprovalSubjectKind) {
	t.Helper()
	if plan == nil {
		t.Fatal("plan is nil")
	}
	if plan.Subject.Kind != kind {
		t.Fatalf("subject = %s, want %s", plan.Subject.Kind, kind)
	}
	seen := map[hitl.ApprovalOptionRung]hitl.ApprovalOption{}
	var quietCount int
	for _, option := range plan.Options {
		if option.Kind == hitl.ApprovalOptionQuiet {
			quietCount++
			if !plan.OptionContinues(option) {
				t.Fatalf("quiet %s does not continue %s: %+v", option.Rung, kind, option.Authority)
			}
			continue
		}
		if option.Group != "" {
			continue
		}
		seen[option.Rung] = option
		if !plan.OptionContinues(option) {
			t.Fatalf("%s option does not continue %s: %+v", option.Rung, kind, option.Authority)
		}
	}
	if quietCount != 0 {
		t.Fatalf("quiet options = %d want 0 when chat lease is enabled", quietCount)
	}
	if _, ok := seen[hitl.ApprovalRungOnce]; !ok {
		t.Fatal("missing Allow once")
	}
	if _, ok := seen[hitl.ApprovalRungDay]; !ok {
		t.Fatal("missing Allow for 1 day")
	}
	if _, ok := seen[hitl.ApprovalRungChat]; !ok {
		t.Fatal("missing Allow for this chat")
	}
	// Local-network authority ends with the chat: the durable slot is present,
	// disabled, and says so.
	if project, ok := seen[hitl.ApprovalRungProject]; !ok || !project.Disabled || project.Note != hitl.NoteEndsWithChat {
		t.Fatalf("durable slot = %+v, want disabled with the ends-with-chat note", project)
	}
	if plan.RecommendedOptionID != seen[hitl.ApprovalRungChat].ID {
		t.Fatalf("face = %q, want the chat lease %q", plan.RecommendedOptionID, seen[hitl.ApprovalRungChat].ID)
	}
	for _, rung := range []hitl.ApprovalOptionRung{hitl.ApprovalRungDay, hitl.ApprovalRungChat} {
		option := seen[rung]
		for _, delta := range option.Authority {
			if len(delta.ListenPorts) != 0 || len(delta.ConnectPorts) != 0 {
				t.Fatalf("%s lease is port-narrowed: listen=%v connect=%v", rung, delta.ListenPorts, delta.ConnectPorts)
			}
		}
	}
}

func TestLocalNetworkPlanContinuesBothAxes(t *testing.T) {
	t.Parallel()
	broker := &LocalNetworkCheckpointBroker{}
	card, err := broker.buildCard(tools.LocalNetworkAsk{
		Command: "node server.js", ToolName: "command", ProjectDir: "/tmp/proj",
		ListenPorts: []uint16{3000}, ConnectPorts: []uint16{3000},
	}, "root", "root")
	testutil.FailErr(t, "buildCard", err)
	if card.Decision == nil || card.Decision.Primary != api.GateCapabilityWidening {
		t.Fatalf("primary = %v, want capability_widening", card.Decision)
	}
	plan := card.Plan
	assertStandardPortRungs(t, plan, hitl.ApprovalSubjectActionSet)
	for _, option := range plan.Options {
		if option.Kind == hitl.ApprovalOptionQuiet {
			continue
		}
		if !plan.OptionContinues(option) {
			t.Fatalf("%s does not continue both axes: %+v", option.Rung, option.Authority)
		}
	}
}

func TestListenPlanKeepsChatAuthorityOnChildSession(t *testing.T) {
	broker := &ListenCheckpointBroker{}
	card, err := broker.buildListenCard(tools.LocalListenAsk{ProjectDir: "/tmp/proj", Ports: []uint16{3000}}, "child", "root", []uint16{3000})
	testutil.FailErr(t, "build listen card", err)
	if card.Action.Scope.RootSessionID != "root" || card.Action.ChatSession() != "root" || card.Action.Scope.SessionID != "child" {
		t.Fatalf("action sessions = root %q chat %q child %q", card.Action.Scope.RootSessionID, card.Action.ChatSession(), card.Action.Scope.SessionID)
	}
	for _, option := range card.Plan.Options {
		for _, delta := range option.Authority {
			if delta.Kind == hitl.AuthorityLocalListenChat && delta.ChatSession() != "root" {
				t.Fatalf("chat authority session = %q, want root", delta.ChatSession())
			}
		}
	}
}

func TestListenDayGrantExpires(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPortGrantRuntime()
	past := time.Now().UTC().Add(-time.Second)
	rt.GrantChat("chat", []uint16{8000}, "grant_day", "cp1", &past)
	if granted, _ := rt.SessionPorts("chat"); granted {
		t.Fatal("expired day grant still authorizes")
	}
	future := time.Now().UTC().Add(time.Hour)
	rt.GrantChat("chat", []uint16{8000}, "grant_live", "cp2", &future)
	granted, ports := rt.SessionPorts("chat")
	if !granted || len(ports) != 1 || ports[0] != 8000 {
		t.Fatalf("live day grant: granted=%v ports=%v", granted, ports)
	}
}
