package promptloop

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReorderToolBatchExecutionPutsTerminalToolsLast(t *testing.T) {
	calls := []api.ToolCall{
		{Name: "wait", Args: map[string]any{"timeout_ms": 60_000}},
		{Name: "read", Args: map[string]any{"path": "x.go"}},
	}
	got := reorderToolBatchExecution(batchTestRegistry(t, "wait", "read"), calls)
	if len(got) != 2 || got[0].Name != "read" || got[1].Name != "wait" {
		t.Fatalf("got = %+v", got)
	}
}

func TestSanitizeToolCallsForExecutionPreservesRepeatableStatefulCalls(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "ts1", Name: "terminal_send", Args: map[string]any{"session_id": "term-1", "keys": []any{"Down"}}},
		{ID: "ts2", Name: "terminal_send", Args: map[string]any{"session_id": "term-1", "keys": []any{"Down"}}},
		{ID: "ts2", Name: "terminal_send", Args: map[string]any{"session_id": "term-1", "keys": []any{"Down"}}},
		{ID: "pa1", Name: "page_act", Args: map[string]any{"page_id": "p1", "action": "press", "key": "ArrowDown"}},
		{ID: "pa2", Name: "page_act", Args: map[string]any{"page_id": "p1", "action": "press", "key": "ArrowDown"}},
		{ID: "w1", Name: "write", Args: map[string]any{"path": "log.txt", "content": "line\n", "append": true}},
		{ID: "w2", Name: "write", Args: map[string]any{"path": "log.txt", "content": "line\n", "append": true}},
		{ID: "wo1", Name: "write", Args: map[string]any{"path": "out.txt", "content": "same"}},
		{ID: "wo2", Name: "write", Args: map[string]any{"path": "out.txt", "content": "same"}},
	}
	got := sanitizeToolCallsForExecution(calls)
	if len(got) != 7 {
		t.Fatalf("calls = %d want 7 (distinct stateful calls kept, duplicate ID ts2 and overwrite wo2 dropped): %+v", len(got), got)
	}
}

func TestWaitVisibilityAtBatchBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, text           string
		note, failNote, park bool
	}{
		{"hidden answer", "The build is still running", false, false, false},
		{"silent wait", "", false, false, true},
		{"note and wait", "", true, false, true},
		{"note covers text", "The build is still running", true, false, true},
		{"rejected note", "The build is still running", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewStubRegistry()
			waitRuns := 0
			testutil.FailErr(t, "register wait", reg.Register("wait", func(_ context.Context, _ map[string]any, ctx tools.ToolContext) (string, error) {
				waitRuns++
				ctx.Effects.Out.Completion = &api.ToolCompletion{Operation: "wait", State: "parked", ResourceKind: "wait"}
				return `{"status":"sleeping"}`, nil
			}))
			testutil.FailErr(t, "register note", reg.Register("surface_note", func(_ context.Context, _ map[string]any, ctx tools.ToolContext) (string, error) {
				if tc.failNote {
					return "", guidance.NewRefusal("TEST_NOTE_REJECTED", "")
				}
				ctx.Effects.Out.AgentNote = &tools.AgentNoteCapture{MessageID: "note-1", Content: "Build running", Grounding: &api.CitationGrounding{Traced: true}}
				return `{"status":"noted"}`, nil
			}))
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Context:    ContextDeps{Tools: reg},
				Projection: ProjectionDeps{AppendMessages: func(context.Context, string, ...api.Message) error { return nil }},
				Tools: ToolsDeps{BeforeToolRun: func(_ context.Context, sess *api.Session, history []api.Message, _, tool string, _ map[string]any) (string, bool, error) {
					gc := oar.NewGuardContext()
					guard.ObserveWaitVisibility(sess, history, tool, true, gc)
					if guard.EvaluateObserveHasCode(gc, oar.AnchorCoordinatorPreInvoke, guard.WaitHiddenTextCode) {
						return "", false, guidance.NewRefusal(guard.WaitHiddenTextCode, "")
					}
					return "", false, nil
				}},
			})
			// Deliberately request wait first; execution must commit the note first.
			calls := []api.ToolCall{{ID: "wait-1", Name: "wait", Args: map[string]any{"timeout_ms": 60000}}}
			if tc.note {
				calls = append(calls, api.ToolCall{ID: "note-1", Name: "surface_note", Args: map[string]any{"summary": "Build running"}})
			}
			sess := &api.Session{ID: "coordinator", Posture: api.SessionPostureBuild}
			history := []api.Message{{ID: "assistant-1", Role: api.MessageRoleAssistant, Content: tc.text, ToolCalls: calls}}
			history, _, _, _, _, parked, err := loop.Batch.executeToolCallsInTurn(context.Background(), sess, sess.ID, calls,
				tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID, Agent: "coordinator"}}, history, "investigate", "assistant-1", "", nil)
			testutil.FailErr(t, "execute wait batch", err)
			if parked != tc.park || (waitRuns == 1) != tc.park {
				t.Fatalf("parked=%v, wait invocations=%d, want park=%v", parked, waitRuns, tc.park)
			}
			if !tc.park {
				found := false
				for _, msg := range history {
					if msg.ToolResult != nil && msg.ToolResult.Tool == "wait" && msg.ToolResult.Outcome == api.ToolResultOutcomeRejected {
						found = true
					}
				}
				if !found {
					t.Fatal("blocked wait must retain a rejected result")
				}
			}
		})
	}
}

const publishingSurface = "implement_routing"

// midTurnLedgerStub is the coordinator ledger a real session would hold. Records
// minted during a test appear only when the test adds them, so a pending handle
// stays unobserved.
type midTurnLedgerStub struct {
	records []evidence.Record
	err     error
}

func (s *midTurnLedgerStub) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	if s.err != nil {
		return evidence.Ledger{}, s.err
	}
	return evidence.AssembleLedger(s.records), nil
}

func (s *midTurnLedgerStub) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

type midTurnHarness struct {
	loop     *PromptLoop
	ledger   *midTurnLedgerStub
	appended []api.Message
	order    []string
	appendFn func(api.Message) error
	notices  []string
	friction int
	kick     string
	sess     *api.Session
	// refuseTask makes the dispatch fail its own validation.
	refuseTask bool
	// budget is the real grounding budget when a test needs its arithmetic.
	budget *closeouts.Service
}

// newMidTurnHarness runs real publication and the real wait observation. Its
// wait refusal stands in for OAR, which TestWaitHiddenTextPolicy covers.
func newMidTurnHarness(t *testing.T, surfaceID string) *midTurnHarness {
	t.Helper()
	h := &midTurnHarness{ledger: &midTurnLedgerStub{}, sess: &api.Session{ID: "coordinator", Posture: api.SessionPostureBuild}}
	reg := tools.NewStubRegistry()
	testutil.FailErr(t, "register recall", reg.Register("recall", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		h.order = append(h.order, "recall")
		return `{"ok":true}`, nil
	}))
	testutil.FailErr(t, "register task", reg.Register("task", func(_ context.Context, _ map[string]any, ctx tools.ToolContext) (string, error) {
		if h.refuseTask {
			return "", guidance.NewRefusal("TOOL_ARGS_INVALID", "refused")
		}
		h.order = append(h.order, "task")
		ctx.Effects.Out.Dispatch = &api.WorkerDispatch{WorkerID: "w1", AgentType: "explore"}
		return `{"job_id":"w1","status":"enqueued"}`, nil
	}))
	testutil.FailErr(t, "register wait", reg.Register("wait", func(_ context.Context, _ map[string]any, ctx tools.ToolContext) (string, error) {
		h.order = append(h.order, "wait")
		ctx.Effects.Out.Completion = &api.ToolCompletion{Operation: "wait", State: "parked", ResourceKind: "wait"}
		return `{"status":"sleeping"}`, nil
	}))
	h.loop = NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{Tools: reg},
		Projection: ProjectionDeps{AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			for _, msg := range msgs {
				if h.appendFn != nil {
					if err := h.appendFn(msg); err != nil {
						return err
					}
				}
				h.appended = append(h.appended, msg)
				if api.IsAgentNoteMessage(msg) {
					h.order = append(h.order, "note")
				}
			}
			return nil
		}},
		Closeout: CloseoutDeps{
			EvidenceLedger: h.ledger,
			RecordGroundingFriction: func(context.Context, string) guidance.GroundingFriction {
				h.friction++
				if h.budget != nil {
					return h.budget.RecordGroundingFriction(context.Background(), h.sess.ID)
				}
				return guidance.GroundingFriction{Remaining: 3}
			},
			EvaluateMidTurnNote: func(_ context.Context, _ *api.Session, gc *oar.GuardContext) error {
				h.notices = append(h.notices, gc.Rejection.RejectObservation)
				return nil
			},
			RenderHostKick: func(_ context.Context, id string, data map[string]any) (string, error) {
				h.kick = id
				workers, _ := data["workers"].([]guidance.MidTurnWorkerFact)
				parts := []string{"Worker status from the host:"}
				for _, worker := range workers {
					parts = append(parts, worker.AgentType+": "+worker.Status)
				}
				return strings.Join(parts, "\n"), nil
			},
		},
		Tools: ToolsDeps{
			BeforeToolRun: func(_ context.Context, sess *api.Session, history []api.Message, _, tool string, _ map[string]any) (string, bool, error) {
				gc := oar.NewGuardContext()
				guard.ObserveWaitVisibility(sess, history, tool, surfaceID == publishingSurface, gc)
				if gc.Session.UnpublishedToolTextPending {
					return "", false, guidance.NewRefusal(guard.WaitHiddenTextCode, "unpublished")
				}
				return "", false, nil
			},
			// A successful call mints what the real ledger would, once it ran.
			CommitEvidenceToolResult: func(_ context.Context, _ string, _ *api.Session, tool string, _ map[string]any, content, _ string) (string, string, error) {
				if tool != "task" {
					return "", content, nil
				}
				handle := "task#1"
				h.ledger.records = append(h.ledger.records, evidence.Record{Handle: handle, Kind: "task", Shape: evidence.ShapeStructuredEvent, Body: []string{content}})
				return handle, content, nil
			},
		},
	})
	return h
}

// respond runs one model response through the real batch.
func (h *midTurnHarness) respond(t *testing.T, history []api.Message, surfaceID, id, text string, calls ...string) ([]api.Message, bool) {
	t.Helper()
	toolCalls := make([]api.ToolCall, 0, len(calls))
	for i, name := range calls {
		toolCalls = append(toolCalls, api.ToolCall{ID: id + "-" + string(rune('a'+i)), Name: name, Args: map[string]any{"timeout_ms": 60000, "path": "a.go"}})
	}
	history = append(history, api.Message{ID: id, Role: api.MessageRoleAssistant, Content: text, ToolCalls: toolCalls})
	out, _, _, _, _, parked, err := h.loop.Batch.executeToolCallsInTurn(context.Background(), h.sess, h.sess.ID, toolCalls,
		tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: h.sess.ID, Agent: "coordinator"}}, history, "go", id, surfaceID, midTurnState(t, surfaceID))
	testutil.FailErr(t, "execute batch", err)
	return out, parked
}

// midTurnState carries the tool plan a coordinator turn compiles for its surface.
func midTurnState(t *testing.T, surfaceID string) *promptLoopTurnState {
	t.Helper()
	st := &promptLoopTurnState{}
	if surfaceID == "" {
		return st
	}
	plans, err := surface.CompileToolPlans(1)
	testutil.FailErr(t, "compile tool plans", err)
	st.turnToolPlan = plans[surfaceID]
	return st
}

func midTurnUser() api.Message {
	return api.Message{ID: "u1", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "go"}
}

func noteFor(history []api.Message, carrierID string) (api.Message, bool) {
	for _, msg := range history {
		if msg.ID == api.MidTurnNoteID(carrierID) {
			return msg, true
		}
	}
	return api.Message{}, false
}

func refusedWaits(history []api.Message) int {
	n := 0
	for _, msg := range history {
		if msg.ToolResult != nil && msg.ToolResult.Tool == "wait" && msg.ToolResult.Outcome == api.ToolResultOutcomeRejected {
			n++
		}
	}
	return n
}

// The failure this design exists to end: text hidden, the coordinator told it
// had not communicated, and wait refused forever. Every model behaviour below
// must leave the wait running within the repair bound.
func TestWaitAfterDispatchCannotLoop(t *testing.T) {
	const bound = 8 // Below BlockedLoopRejectCap's reach in every branch.
	for _, tc := range []struct {
		name string
		next func(step int) string
	}{
		{"repeats the same uncited text", func(int) string { return "Worker started, waiting." }},
		{"changes its uncited text", func(step int) string { return "Waiting, attempt " + string(rune('a'+step)) }},
		{"goes silent", func(int) string { return "" }},
		{"cites a handle that never existed", func(int) string { return "Done [task#99]." }},
		{"writes malformed syntax", func(int) string { return "See [read#3:9-2]." }},
		{"parrots only the old framing", func(int) string { return "⟦D:model:internal_step_note⟧\n⟦D⟧" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMidTurnHarness(t, publishingSurface)
			history, _ := h.respond(t, []api.Message{midTurnUser()}, publishingSurface, "c0", "Dispatching a worker.", "task")
			if _, ok := noteFor(history, "c0"); !ok {
				t.Fatal("pre-evidence update did not publish")
			}
			history, parked := h.respond(t, history, publishingSurface, "c1", "Worker started, waiting.", "wait")
			if parked {
				t.Fatal("uncited update after a dispatch must not pass silently")
			}
			if len(h.notices) != 1 || h.notices[0] != strings.ToLower(guidance.MidTurnNoteUngroundedCode) {
				t.Fatalf("notices = %v", h.notices)
			}
			steps := 1
			for !parked {
				if steps >= bound {
					t.Fatalf("wait still refused after %d responses", steps)
				}
				history, parked = h.respond(t, history, publishingSurface, "r"+string(rune('a'+steps)), tc.next(steps), "wait")
				steps++
			}
			if got := refusedWaits(history); got > limits.DefaultWaitVisibilityRepairs {
				t.Fatalf("refused waits = %d, bound %d", got, limits.DefaultWaitVisibilityRepairs)
			}
			var hostNote *api.Message
			for i := range history {
				if api.IsAgentNoteMessage(history[i]) && history[i].Origin == api.MessageOriginHost {
					hostNote = &history[i]
				}
			}
			if hostNote == nil || !strings.Contains(hostNote.Content, "explore: started") {
				t.Fatalf("host progress note missing: %+v", hostNote)
			}
			if !strings.Contains(strings.Join(h.notices, ","), strings.ToLower(guidance.MidTurnNoteWaitFallbackCode)) {
				t.Fatalf("fallback not recorded: %v", h.notices)
			}
		})
	}
}

// The ordinary recovery: the handle the dispatch printed publishes at once.
func TestNoteRightAfterDispatchPublishes(t *testing.T) {
	h := newMidTurnHarness(t, publishingSurface)
	history, _ := h.respond(t, []api.Message{midTurnUser()}, publishingSurface, "c0", "Dispatching a worker.", "task")
	history, parked := h.respond(t, history, publishingSurface, "c1", "Worker started [task#1], waiting on it.", "wait")
	if !parked || refusedWaits(history) != 0 {
		t.Fatalf("parked=%v refused=%d", parked, refusedWaits(history))
	}
	note, ok := noteFor(history, "c1")
	if !ok || strings.Contains(note.Content, "task#1") || !strings.Contains(note.Content, "waiting on it") || note.Grounding == nil || !note.Grounding.Traced {
		t.Fatalf("note = %+v", note)
	}
}

// A dispatch that was refused mints nothing to cite.
func TestRefusedDispatchProducesNoCitableRecord(t *testing.T) {
	h := newMidTurnHarness(t, publishingSurface)
	h.refuseTask = true
	history, _ := h.respond(t, []api.Message{midTurnUser()}, publishingSurface, "c0", "Dispatching.", "task")
	if len(h.ledger.records) != 0 {
		t.Fatalf("refused dispatch minted %+v", h.ledger.records)
	}
	for _, msg := range history {
		if len(msg.EvidenceHandles) > 0 {
			t.Fatalf("refused dispatch carries handles: %+v", msg)
		}
	}
}

func TestToolEvidenceEligibleRequiresAcceptedDispatch(t *testing.T) {
	succeeded := toolInvocation{facts: guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeCompleted}}
	if toolEvidenceEligible(succeeded, "task", nil) {
		t.Fatal("a task result without a dispatch outcome is not a dispatch record")
	}
	succeeded.captures.dispatch = &api.WorkerDispatch{WorkerID: " "}
	if toolEvidenceEligible(succeeded, "delegate_dispatch", nil) {
		t.Fatal("a dispatch needs worker identity")
	}
	succeeded.captures.dispatch = &api.WorkerDispatch{WorkerID: "w1"}
	if !toolEvidenceEligible(succeeded, "task", nil) || !toolEvidenceEligible(succeeded, "delegate_dispatch", nil) {
		t.Fatal("an accepted dispatch records evidence")
	}
}

func TestPublicationPrecedesToolsAndToolsAlwaysRun(t *testing.T) {
	seeded := []evidence.Record{{Handle: "read#3", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", LineRanges: []evidence.LineRange{{Start: 40, End: 50}}}}
	observed := api.Message{ID: "t0", Role: api.MessageRoleTool, EvidenceHandles: []string{"read#3"}, ToolResult: &api.ToolResult{Tool: "read"}}
	for _, tc := range []struct {
		name     string
		text     string
		observed bool
		note     bool
		notice   string
		friction int
	}{
		{name: "AC-MID-03 cited update commits before the tool", text: "Found it in [read#3:42].", observed: true, note: true},
		{name: "AC-MID-04 uncited withholds and the tool runs", text: "Still looking.", observed: true, notice: guidance.MidTurnNoteUngroundedCode},
		{name: "AC-MID-05 invalid withholds and the tool runs", text: "See [read#9:1].", observed: true, notice: guidance.MidTurnNoteHandleUngroundCode, friction: 1},
		{name: "AC-MID-13 parroted header is no text", text: "⟦D:model:internal_step_note⟧\n"},
		{name: "AC-MID-13 empty step is ordinary", text: " \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMidTurnHarness(t, publishingSurface)
			h.ledger.records = seeded
			history := []api.Message{midTurnUser()}
			if tc.observed {
				history = append(history, observed)
			}
			out, _ := h.respond(t, history, publishingSurface, "c1", tc.text, "recall")
			want := []string{"recall"}
			if tc.note {
				want = []string{"note", "recall"}
			}
			if strings.Join(h.order, ",") != strings.Join(want, ",") {
				t.Fatalf("order = %v, want %v", h.order, want)
			}
			if _, ok := noteFor(out, "c1"); ok != tc.note {
				t.Fatalf("note published = %v", ok)
			}
			if tc.notice == "" && len(h.notices) != 0 || tc.notice != "" && (len(h.notices) != 1 || h.notices[0] != strings.ToLower(tc.notice)) {
				t.Fatalf("notices = %v, want one %s", h.notices, tc.notice)
			}
			if h.friction != tc.friction {
				t.Fatalf("friction = %d, want %d", h.friction, tc.friction)
			}
			if !tc.note && guard.DeriveWaitVisibility(out).Unpublished != (tc.notice != "") {
				t.Fatalf("debt = %+v", guard.DeriveWaitVisibility(out))
			}
		})
	}
}

// AC-MID-14, AC-MID-19: private text is neither published nor owed.
func TestPrivateTextPublishesNothingAndOwesNothing(t *testing.T) {
	for _, tc := range []struct {
		name, surface, tool string
		worker              bool
	}{
		{"synthesis surface", "implement_synthesis", "recall", false},
		{"root session on no surface", "", "wait", false},
		{"worker on a publishing surface", publishingSurface, "wait", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMidTurnHarness(t, tc.surface)
			if tc.worker {
				h.sess.ParentSessionID = "parent"
			}
			out, _ := h.respond(t, []api.Message{midTurnUser()}, tc.surface, "c1", "Private thought.", tc.tool)
			if strings.Join(h.order, ",") != tc.tool {
				t.Fatalf("private text changed the batch: %v", h.order)
			}
			if _, ok := noteFor(out, "c1"); ok || len(h.notices) != 0 || h.friction != 0 {
				t.Fatalf("published=%v notices=%v friction=%d", ok, h.notices, h.friction)
			}
		})
	}
}

// AC-MID-10: a failed commit claims nothing and runs nothing; a duplicate is the
// earlier commit, which is reused.
func TestNoteStorageOutcomes(t *testing.T) {
	t.Run("storage failure stops before any tool", func(t *testing.T) {
		h := newMidTurnHarness(t, publishingSurface)
		boom := errors.New("disk full")
		h.appendFn = func(msg api.Message) error {
			if api.IsAgentNoteMessage(msg) {
				return boom
			}
			return nil
		}
		history := []api.Message{midTurnUser(), {ID: "c1", Role: api.MessageRoleAssistant, Content: "Starting.", ToolCalls: []api.ToolCall{{ID: "e", Name: "recall"}}}}
		out, _, _, _, _, _, err := h.loop.Batch.executeToolCallsInTurn(context.Background(), h.sess, h.sess.ID, history[1].ToolCalls,
			tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: h.sess.ID}}, history, "go", "c1", publishingSurface, midTurnState(t, publishingSurface))
		if !errors.Is(err, boom) || len(h.order) != 0 {
			t.Fatalf("err=%v order=%v", err, h.order)
		}
		if _, ok := noteFor(out, "c1"); ok || !guard.DeriveWaitVisibility(out).Unpublished {
			t.Fatal("a failed commit must stay unpublished")
		}
	})
	t.Run("duplicate reuses the committed note", func(t *testing.T) {
		h := newMidTurnHarness(t, publishingSurface)
		h.appendFn = func(msg api.Message) error {
			if api.IsAgentNoteMessage(msg) {
				return store.ErrDuplicateMessageID
			}
			return nil
		}
		out, _ := h.respond(t, []api.Message{midTurnUser()}, publishingSurface, "c1", "Starting.", "recall")
		if _, ok := noteFor(out, "c1"); !ok || strings.Join(h.order, ",") != "recall" {
			t.Fatalf("order=%v", h.order)
		}
	})
	t.Run("a note already in history is not published twice", func(t *testing.T) {
		h := newMidTurnHarness(t, publishingSurface)
		existing := api.Message{ID: api.MidTurnNoteID("c1"), Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Starting."}
		out, _ := h.respond(t, []api.Message{midTurnUser(), existing}, publishingSurface, "c1", "Starting.", "recall")
		count := 0
		for _, msg := range out {
			if msg.ID == existing.ID {
				count++
			}
		}
		if count != 1 || strings.Join(h.order, ",") != "recall" {
			t.Fatalf("count=%d order=%v", count, h.order)
		}
	})
}

// Evidence that cannot be read is not an empty ledger: it must not exempt text.
func TestUnreadableLedgerNeverPublishesAsExempt(t *testing.T) {
	h := newMidTurnHarness(t, publishingSurface)
	h.ledger.err = errors.New("ledger offline")
	out, _ := h.respond(t, []api.Message{midTurnUser()}, publishingSurface, "c1", "Starting.", "recall")
	if _, ok := noteFor(out, "c1"); ok || strings.Join(h.order, ",") != "recall" {
		t.Fatalf("published=%v order=%v", ok, h.order)
	}
	if !guard.DeriveWaitVisibility(out).Unpublished {
		t.Fatal("unpublished text must be owed")
	}
}

// With no worker facts the host has nothing true to say, so the wait is silent.
func TestExhaustedRepairWithoutFactsPermitsSilentWait(t *testing.T) {
	h := newMidTurnHarness(t, publishingSurface)
	h.ledger.records = []evidence.Record{{Handle: "read#3", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", LineRanges: []evidence.LineRange{{Start: 1, End: 2}}}}
	history := []api.Message{midTurnUser(), {ID: "t0", Role: api.MessageRoleTool, EvidenceHandles: []string{"read#3"}, ToolResult: &api.ToolResult{Tool: "read"}}}
	parked := false
	for step := 0; !parked; step++ {
		if step > limits.DefaultWaitVisibilityRepairs+1 {
			t.Fatal("wait never permitted")
		}
		history, parked = h.respond(t, history, publishingSurface, "c"+string(rune('a'+step)), "Still uncited.", "wait")
	}
	for _, msg := range history {
		if api.IsAgentNoteMessage(msg) {
			t.Fatalf("host invented speech: %+v", msg)
		}
	}
	if h.kick != "" {
		t.Fatalf("rendered %q with no facts", h.kick)
	}
}

// A note goes through the same storage screen as every row: what lands on disk
// is the redacted revision, and the carrier's text is what was audited.
func TestPublishedNoteUsesTheStorageSafeRevision(t *testing.T) {
	h := newMidTurnHarness(t, publishingSurface)
	h.loop.Projection.Deps.RedactMessageForStorage = func(_ context.Context, msg api.Message) (api.Message, bool) {
		if !api.IsAgentNoteMessage(msg) {
			return msg, false
		}
		msg.Content = strings.ReplaceAll(msg.Content, "s3cret", "[redacted]")
		return msg, true
	}
	h.respond(t, []api.Message{midTurnUser()}, publishingSurface, "c1", "The key is s3cret.", "recall")
	for _, msg := range h.appended {
		if api.IsAgentNoteMessage(msg) {
			if msg.Content != "The key is [redacted]." {
				t.Fatalf("stored note = %q", msg.Content)
			}
			return
		}
	}
	t.Fatal("no note stored")
}

// A host progress note is labelled data for the model, not its own speech.
func TestHostProgressNoteProjectsAsHostData(t *testing.T) {
	h := newMidTurnHarness(t, publishingSurface)
	history, _ := h.respond(t, []api.Message{midTurnUser()}, publishingSurface, "c0", "Dispatching.", "task")
	history, _ = h.respond(t, history, publishingSurface, "c1", "Uncited.", "wait")
	history, parked := h.respond(t, history, publishingSurface, "c2", "Still uncited.", "wait")
	if !parked {
		t.Fatal("wait must proceed after repair")
	}
	var host api.Message
	for _, msg := range api.FilterPromptHistory(history) {
		if api.IsAgentNoteMessage(msg) && msg.Origin == api.MessageOriginHost {
			host = msg
		}
	}
	if host.ID == "" {
		t.Fatal("host note missing from model history")
	}
	projected := transcript.Project([]api.Message{host})
	got := projected[len(projected)-1].Content
	if !strings.HasPrefix(got, "Data from host:\n") || !strings.Contains(got, "explore: started") {
		t.Fatalf("host note projection = %q", got)
	}
	if strings.Contains(host.Content, "Uncited") {
		t.Fatal("the host must not repeat the model's unpublished text")
	}
}

type rootSessionStub struct{}

func (rootSessionStub) Get(_ context.Context, id string) (*api.Session, error) {
	return &api.Session{ID: id}, nil
}

// AC-MID-15: narration costs closeout nothing; an invalid reference spends the
// one pool closeouts draw on, with the closeout limits themselves unchanged.
func TestMidTurnTextAgainstTheCloseoutRepairBudget(t *testing.T) {
	observed := api.Message{ID: "t0", Role: api.MessageRoleTool, EvidenceHandles: []string{"read#3"}, ToolResult: &api.ToolResult{Tool: "read"}}
	for _, tc := range []struct {
		name, text string
		steps      int
		charged    int
	}{
		{"uncited narration across many tool steps", "Now checking the next file.", 3 * limits.DefaultGroundingRejectsPerCycle, 0},
		{"invalid citations", "See [read#9:1].", 2, 2},
		{"malformed syntax", "See [read#3:9-2].", 3, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMidTurnHarness(t, publishingSurface)
			h.budget = closeouts.New(rootSessionStub{})
			h.ledger.records = []evidence.Record{{Handle: "read#3", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", LineRanges: []evidence.LineRange{{Start: 1, End: 9}}}}
			history := []api.Message{midTurnUser(), observed}
			for step := range tc.steps {
				history, _ = h.respond(t, history, publishingSurface, "c"+strconv.Itoa(step), tc.text, "recall")
			}
			if len(h.order) != tc.steps || len(h.notices) != tc.steps {
				t.Fatalf("tools ran %d, notices %d, want %d of each", len(h.order), len(h.notices), tc.steps)
			}
			if h.friction != tc.charged {
				t.Fatalf("charged %d, want %d", h.friction, tc.charged)
			}
			// The closeout's own first rejection sees whatever the note audit left.
			got := h.budget.RecordGroundingFriction(context.Background(), h.sess.ID).Remaining
			if want := limits.DefaultGroundingRejectsPerCycle - tc.charged - 1; got != want {
				t.Fatalf("closeout budget remaining = %d, want %d", got, want)
			}
		})
	}
}

func countNotices(h *midTurnHarness, code string) int {
	n := 0
	for _, notice := range h.notices {
		if notice == guidance.MidTurnObservation(code) {
			n++
		}
	}
	return n
}

// The fallback notice belongs to a debt, not to each wait. It survives a restart
// because it derives from durable rows.
func TestWaitFallbackNoticeFiresOncePerDebt(t *testing.T) {
	h := newMidTurnHarness(t, publishingSurface)
	h.ledger.records = []evidence.Record{{Handle: "read#3", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", LineRanges: []evidence.LineRange{{Start: 1, End: 9}}}}
	history := []api.Message{midTurnUser(), {ID: "t0", Role: api.MessageRoleTool, EvidenceHandles: []string{"read#3"}, ToolResult: &api.ToolResult{Tool: "read"}}}
	step := func(id, text string, wantParked bool, wantFallbacks int) {
		t.Helper()
		var parked bool
		history, parked = h.respond(t, history, publishingSurface, id, text, "wait")
		if parked != wantParked {
			t.Fatalf("%s: parked = %v", id, parked)
		}
		if got := countNotices(h, guidance.MidTurnNoteWaitFallbackCode); got != wantFallbacks {
			t.Fatalf("%s: fallback notices = %d, want %d: %v", id, got, wantFallbacks, h.notices)
		}
	}
	step("c1", "Uncited.", false, 0)
	step("c2", "", true, 1)
	step("c3", "", true, 1)
	step("c4", "", true, 1)
	// A fresh process reads the same rows and stays quiet.
	h = newMidTurnHarness(t, publishingSurface)
	h.ledger.records = []evidence.Record{{Handle: "read#3", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", LineRanges: []evidence.LineRange{{Start: 1, End: 9}}}}
	step("c5", "", true, 0)
	// Newer unpublished text is new debt.
	step("c6", "New uncited text.", true, 1)
	step("c7", "", true, 1)
	// A note settles everything, and later silence owes nothing.
	step("c8", "Published [read#3:2].", true, 1)
	step("c9", "", true, 1)
	if _, ok := noteFor(history, "c8"); !ok {
		t.Fatal("cited update did not publish")
	}
}

// A model that never cites must not produce a stream of host speech, even as
// worker facts change and each host note clears the debt it answered.
func TestHostProgressNoteAtMostOncePerIntent(t *testing.T) {
	h := newMidTurnHarness(t, publishingSurface)
	history, _ := h.respond(t, []api.Message{midTurnUser()}, publishingSurface, "c0", "Dispatching.", "task")
	hostNotes := func() int {
		n := 0
		for _, msg := range history {
			if api.IsAgentNoteMessage(msg) && msg.Origin == api.MessageOriginHost {
				n++
			}
		}
		return n
	}
	for cycle := range 6 {
		history = append(history, api.Message{ID: "w" + strconv.Itoa(cycle), Role: api.MessageRoleUser, Visibility: api.MessageVisibilityInternal,
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "w" + strconv.Itoa(cycle), AgentType: "explore", Status: "completed"}})
		history, _ = h.respond(t, history, publishingSurface, "a"+strconv.Itoa(cycle), "Uncited, attempt "+strconv.Itoa(cycle), "wait")
		history, _ = h.respond(t, history, publishingSurface, "b"+strconv.Itoa(cycle), "Uncited again "+strconv.Itoa(cycle), "wait")
	}
	if got := hostNotes(); got != 1 {
		t.Fatalf("host notes in one intent = %d", got)
	}
	// A new request is a new conversation with the person.
	history = append(history, api.Message{ID: "u2", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "again"})
	history, _ = h.respond(t, history, publishingSurface, "d0", "Dispatching.", "task")
	history, _ = h.respond(t, history, publishingSurface, "d1", "Uncited.", "wait")
	history, _ = h.respond(t, history, publishingSurface, "d2", "Uncited.", "wait")
	if got := hostNotes(); got != 2 {
		t.Fatalf("host notes after a new intent = %d", got)
	}
}
