package session

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/toolcontext"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLiveProjectionCoalescesAndSkipsOutbox(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "live-projection.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())

	hub := events.NewMemoryHub()
	outbox := eventoutbox.New(sqlDB, hub)
	outbox.Start(t.Context())
	t.Cleanup(func() { _ = outbox.Close() })

	st := store.NewSQL(sqlDB)
	st.SetEventOutbox(outbox)
	pub := &events.Publisher{Hub: hub}
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetEventPublisher(pub)

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	waitOutboxEmpty(t, sqlDB)

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	t.Cleanup(unsub)

	msgID := "live-draft-1"
	testutil.FailErr(t, "append placeholder", st.AppendMessages(ctx, sess.ID, api.Message{
		ID:          msgID,
		Role:        api.MessageRoleAssistant,
		DraftStatus: api.DraftStatusLive,
		Visibility:  api.MessageVisibilityInternal,
		CreatedAt:   time.Now().UTC(),
	}))
	waitOutboxEmpty(t, sqlDB)
	drainMessageEvents(t, ch)

	for i := 1; i <= 20; i++ {
		testutil.FailErr(t, "project live", mgr.Runner.Transcript.Streams.Project(ctx, sess.ID, api.Message{
			ID:      msgID,
			Role:    api.MessageRoleAssistant,
			Content: "token-body-" + strconv.Itoa(i),
		}))
	}
	mgr.Runner.Transcript.Streams.Flush(ctx, sess.ID)

	patches := collectMessageOps(t, ch)
	if n := patches[api.MessageChangePatch]; n != 0 {
		t.Fatalf("live SSE patches on project hub = %d want 0", n)
	}
	if n := patches[api.MessageChangeAppend]; n != 0 {
		t.Fatalf("unexpected appends after placeholder drain: %d", n)
	}

	stored, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(stored) != 1 || stored[0].Content != "token-body-20" {
		t.Fatalf("stored live content = %q want last token body", contentOrEmpty(stored))
	}
	if stored[0].Seq != 1 {
		t.Fatalf("live persist advanced seq = %d want 1 (append seq)", stored[0].Seq)
	}

	var outboxRows int
	testutil.FailErr(t, "count outbox", sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM event_outbox`).Scan(&outboxRows))
	if outboxRows != 0 {
		t.Fatalf("outbox rows after live projection = %d want 0", outboxRows)
	}

	testutil.FailErr(t, "settle", mgr.Runner.Transcript.Update(ctx, sess.ID, msgID, api.Message{
		ID:         msgID,
		Role:       api.MessageRoleAssistant,
		Content:    "token-body-20",
		Visibility: api.MessageVisibilityTranscript,
	}))
	waitOutboxEmpty(t, sqlDB)
	settled := collectMessageOps(t, ch)
	if settled[api.MessageChangePatch] < 1 {
		t.Fatal("settle did not publish a durable patch")
	}
}

func TestLiveProjectionPersistsLongRunningToolCallsWithoutWiping(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "live-projection-calls.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())

	hub := events.NewMemoryHub()
	outbox := eventoutbox.New(sqlDB, hub)
	outbox.Start(t.Context())
	t.Cleanup(func() { _ = outbox.Close() })

	st := store.NewSQL(sqlDB)
	st.SetEventOutbox(outbox)
	pub := &events.Publisher{Hub: hub}
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetEventPublisher(pub)

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	waitOutboxEmpty(t, sqlDB)

	msgID := "live-draft-calls"
	testutil.FailErr(t, "append placeholder", st.AppendMessages(ctx, sess.ID, api.Message{
		ID:          msgID,
		Role:        api.MessageRoleAssistant,
		DraftStatus: api.DraftStatusLive,
		Visibility:  api.MessageVisibilityInternal,
		CreatedAt:   time.Now().UTC(),
	}))
	waitOutboxEmpty(t, sqlDB)

	testutil.FailErr(t, "project live tool_calls", mgr.Runner.Transcript.Streams.Project(ctx, sess.ID, api.Message{
		ID:      msgID,
		Role:    api.MessageRoleAssistant,
		Content: "running",
		ToolCalls: []api.ToolCall{{
			ID:   "tc-cmd",
			Name: "command",
			Args: map[string]any{"command": "ls"},
		}},
	}))
	mgr.Runner.Transcript.Streams.Flush(ctx, sess.ID)
	withCalls, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after tool_calls", err)
	if len(withCalls[0].ToolCalls) != 1 || withCalls[0].ToolCalls[0].Name != "command" {
		t.Fatalf("live tool_calls = %+v", withCalls[0].ToolCalls)
	}
	testutil.FailErr(t, "project live content only", mgr.Runner.Transcript.Streams.Project(ctx, sess.ID, api.Message{
		ID:      msgID,
		Role:    api.MessageRoleAssistant,
		Content: "still running",
	}))
	mgr.Runner.Transcript.Streams.Flush(ctx, sess.ID)
	kept, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after content-only", err)
	if kept[0].Content != "still running" {
		t.Fatalf("content after content-only live patch = %q", kept[0].Content)
	}
	if len(kept[0].ToolCalls) != 1 || kept[0].ToolCalls[0].Name != "command" {
		t.Fatalf("content-only live patch wiped tool_calls: %+v", kept[0].ToolCalls)
	}
}

func contentOrEmpty(msgs []api.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[0].Content
}

func drainMessageEvents(t *testing.T, ch <-chan api.EventEnvelope) {
	t.Helper()
	_ = collectMessageOps(t, ch)
}

func collectMessageOps(t *testing.T, ch <-chan api.EventEnvelope) map[api.MessageChangeOp]int {
	t.Helper()
	got := map[api.MessageChangeOp]int{}
	for {
		select {
		case env := <-ch:
			if env.Topic != api.EventTopicMessage {
				continue
			}
			var ev api.MessageEvent
			if err := json.Unmarshal(env.Data, &ev); err != nil {
				testutil.FailErr(t, "decode message event", err)
			}
			got[ev.Op]++
		default:
			return got
		}
	}
}

func waitOutboxEmpty(t *testing.T, database db.Handle) {
	t.Helper()
	for t.Context().Err() == nil {
		var count int
		if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM event_outbox").Scan(&count); err == nil && count == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("event outbox did not drain")
}

func TestAttachSkillReadRootsPrefersMachine(t *testing.T) {
	tctx := &tools.ToolContext{}
	(&toolcontext.Service{}).AttachSkillReadRoots(context.Background(), nil, "", inject.Machine{
		ProfileID: "coordinator",
		ReadRoots: []string{"/skills/a", "/skills/b"},
	}, tctx)
	if len(tctx.Files.ReadRoots) != 2 || tctx.Files.ReadRoots[0] != "/skills/a" || tctx.Files.ReadRoots[1] != "/skills/b" {
		t.Fatalf("ReadRoots = %v", tctx.Files.ReadRoots)
	}
}

func TestCompileMachineZeroSession(t *testing.T) {
	var mgr *profiles.Service
	if got := mgr.CompileMachine(context.Background(), nil, "coordinator"); got.Compiled() {
		t.Fatalf("nil compile = %+v", got)
	}
}

func TestPromptSurfaceWriteAgentSeesOmitAllowPresence(t *testing.T) {
	mgr := &profiles.Service{}
	sess := &api.Session{ID: "s1", AgentType: "implementer"}
	snapshot := hostresources.Snapshot{Resources: []hostresources.State{{
		ID: "docker", Label: "Docker", Category: "Containers",
		Status: hostresources.StatusAvailable, HostSupport: hostresources.HostSupported,
		Access: hostresources.AccessAllow, Prompt: hostresources.PromptOmit,
		Surfaces: []hostresources.ExecutionSurface{hostresources.SurfaceProcessExec},
	}}}
	surfaces := []hostresources.ExecutionSurface{hostresources.SurfaceProcessExec}
	write := mgr.PromptSurface(context.Background(), sess, nil, snapshot, surfaces, true)
	if len(write.HostResources) != 1 || write.HostResources[0].ID != "docker" {
		t.Fatalf("write-agent surface = %#v", write.HostResources)
	}
	if write.HostResources[0].Category != "Containers" {
		t.Fatalf("category = %q", write.HostResources[0].Category)
	}
	read := mgr.PromptSurface(context.Background(), sess, nil, snapshot, surfaces, false)
	if len(read.HostResources) != 0 {
		t.Fatalf("read-only surface = %#v", read.HostResources)
	}
}

type occupancyTurnClient struct {
	modelcall.LLMClient
	contexts []context.Context
}

func (c *occupancyTurnClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	c.contexts = append(c.contexts, ctx)
	return c.LLMClient.Stream(ctx, req)
}

func TestAdmittedTurnCarriesUtilityOccupancyToProvider(t *testing.T) {
	mgr, st := newTestManager(t)
	client := &occupancyTurnClient{LLMClient: mgr.Coordinator.Model.LLM}
	mgr.Coordinator.Model.LLM = client
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create admitted session", err)
	_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "Continue the investigation.")
	testutil.FailErr(t, "execute admitted prompt", err)
	if len(client.contexts) == 0 {
		t.Fatal("admitted prompt never reached the mock provider")
	}
	for _, ctx := range client.contexts {
		identity := curationctx.SessionFrom(ctx)
		if !curationctx.LaneOccupied(ctx) || identity.SessionID != sess.ID || identity.ProjectID != sess.ProjectID {
			t.Fatalf("provider context lost admitted utility occupancy or identity: occupied=%v identity=%+v", curationctx.LaneOccupied(ctx), identity)
		}
	}
	if curationctx.LaneOccupied(t.Context()) || curationctx.SessionFrom(t.Context()).SessionID != "" {
		t.Fatal("turn occupancy escaped into the caller's context")
	}
}

const messageStorageSecret = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"

func TestUpdateMessageReappliesStorageRedaction(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.Runner.Transcript.SetRedactor(redactMessageStorageFixture)
	sess, err := mem.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	placeholder := api.Message{ID: "assistant-1", Role: api.MessageRoleAssistant}
	testutil.FailErr(t, "append placeholder", mgr.Runner.Transcript.Append(ctx, sess.ID, placeholder))
	raw := placeholder
	raw.ToolCalls = []api.ToolCall{{
		ID: "call-write", Name: "write",
		Args: map[string]any{"content": "TOKEN=" + messageStorageSecret},
	}}
	testutil.FailErr(t, "update assistant tool call", mgr.Runner.Transcript.Update(ctx, sess.ID, raw.ID, raw))

	messages, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read messages", err)
	if len(messages) != 1 || len(messages[0].ToolCalls) != 1 {
		t.Fatalf("stored messages = %+v", messages)
	}
	content, _ := messages[0].ToolCalls[0].Args["content"].(string)
	if strings.Contains(content, messageStorageSecret) || !strings.Contains(content, "[REDACTED]") {
		t.Fatalf("stored tool-call content = %q", content)
	}
	if messages[0].HostSecretRedaction == nil || messages[0].HostSecretRedaction.Occurrences() != 1 {
		t.Fatalf("redaction provenance = %+v", messages[0].HostSecretRedaction)
	}
}

func redactMessageStorageFixture(_ context.Context, msg api.Message) (api.Message, bool) {
	out := msg
	changed := false
	if len(msg.ToolCalls) > 0 {
		out.ToolCalls = append([]api.ToolCall(nil), msg.ToolCalls...)
		for i := range out.ToolCalls {
			out.ToolCalls[i].Args = make(map[string]any, len(msg.ToolCalls[i].Args))
			for key, value := range msg.ToolCalls[i].Args {
				if text, ok := value.(string); ok {
					redacted := strings.ReplaceAll(text, messageStorageSecret, "[REDACTED]")
					changed = changed || redacted != text
					value = redacted
				}
				out.ToolCalls[i].Args[key] = value
			}
		}
	}
	if changed {
		out.HostSecretRedaction = api.NewHostSecretRedactionMeta([]api.RedactedSpan{{Field: "content", Start: 0, Length: 10, Kind: api.RedactionKindSecret}})
	}
	return out, changed
}

// A phase the coordinator enters with its own tool call is taught on its next
// model call, even while another kick waits for the next turn start.
func TestPhaseGuidanceIsDeliveredOnTheNextModelCall(t *testing.T) {
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	const phaseGuidance = "claims phase fixture"
	testutil.FailErr(t, "register phase fixture", engine.Register("kicks/coordinator-security-claims.md", phaseGuidance))
	mgr.SetPromptEngine(engine)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)
	kicks := mgr.Coordinator.Runtime.Kicks()
	staged := anchor.InformRender(anchor.ComposeDone)
	kicks.QueueDeferred(sess.ID, staged)
	if got := kicks.TakePendingKickID(sess.ID); got != staged {
		t.Fatalf("staged = %q", got)
	}
	kicks.QueueDeferredLatest(sess.ID, anchor.PhaseEntered.String(), "coordinator-security-claims")

	msgs, err := mgr.Coordinator.Guidance.TakePhase(t.Context(), sess.ID)
	testutil.FailErr(t, "take phase guidance", err)
	if len(msgs) != 1 || msgs[0].Content != phaseGuidance || msgs[0].Visibility != api.MessageVisibilityInternal {
		t.Fatalf("delivered = %+v, want the claims phase guidance", msgs)
	}
	stored, err := st.GetMessage(t.Context(), sess.ID, msgs[0].ID)
	testutil.FailErr(t, "read delivered guidance", err)
	if stored.Content != msgs[0].Content {
		t.Fatal("phase guidance was not persisted")
	}
	if again, err := mgr.Coordinator.Guidance.TakePhase(t.Context(), sess.ID); err != nil || len(again) != 0 {
		t.Fatalf("second take = %+v %v, want the guidance delivered once", again, err)
	}
	if got, ok := kicks.PeekPendingKickID(sess.ID); !ok || got != staged {
		t.Fatalf("the kick staged for the turn start = %q %v", got, ok)
	}
}
