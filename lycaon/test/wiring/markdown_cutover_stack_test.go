package wiring

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type stackStep struct {
	text string
	tool string
	args map[string]any
	fail error
}

// stackScriptLLM answers coordinator requests in order and utility requests with
// plain text. After its steps it repeats tail, which may vary by call.
type stackScriptLLM struct {
	mu    sync.Mutex
	steps []stackStep
	next  int
	tail  func(call int) stackStep
	calls int
}

func (m *stackScriptLLM) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if len(req.Tools) == 0 {
		return &modelcall.Completion{Content: "Chat."}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	step := stackStep{text: "Finished."}
	switch {
	case m.next < len(m.steps):
		step = m.steps[m.next]
		m.next++
	case m.tail != nil:
		step = m.tail(m.calls)
	}
	if step.fail != nil {
		return nil, step.fail
	}
	out := &modelcall.Completion{Content: step.text}
	if step.tool != "" {
		out.ToolCalls = []wire.ToolCall{{ID: "call-" + strconv.Itoa(m.calls), Name: step.tool, Args: step.args}}
	}
	return out, nil
}

func (m *stackScriptLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	out, err := m.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: out.Content, ToolCalls: out.ToolCalls, Done: true}
	close(ch)
	return ch, nil
}

func commandStep(text, command string) stackStep {
	return stackStep{text: text, tool: "command", args: map[string]any{"command": command}}
}

func waitStep(text string) stackStep {
	return stackStep{text: text, tool: "wait", args: map[string]any{
		"conditions": []any{map[string]any{"kind": "all_workers_idle"}},
	}}
}

// runStack drives one prompt on the wiring stack against a directory holding notes.txt.
func runStack(t *testing.T, script *stackScriptLLM, prompt string) (*Harness, *wire.Session, string, error) {
	t.Helper()
	t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	h := BuildForTest(t, WithLLMClient(script))
	dir := t.TempDir()
	testutil.FailErr(t, "seed notes", os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("first note\nsecond note\n"), 0o644))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, t.Context(), sess.ID)
	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, prompt)
	return h, sess, dir, err
}

func stackMessages(t *testing.T, h *Harness, sessionID string) []wire.Message {
	t.Helper()
	msgs, err := h.Sessions.Manager.Runner.Transcript.GetMessages(t.Context(), sessionID)
	testutil.FailErr(t, "read messages", err)
	return msgs
}

func toolResults(msgs []wire.Message, tool string, outcome wire.ToolResultOutcome) []wire.Message {
	var out []wire.Message
	for _, msg := range msgs {
		if msg.ToolResult != nil && msg.ToolResult.Tool == tool && msg.ToolResult.Outcome == outcome {
			out = append(out, msg)
		}
	}
	return out
}

func agentNotes(msgs []wire.Message) []wire.Message {
	var out []wire.Message
	for _, msg := range msgs {
		if wire.IsAgentNoteMessage(msg) {
			out = append(out, msg)
		}
	}
	return out
}

func codeCount(msgs []wire.Message, code string) int {
	n := 0
	for _, msg := range msgs {
		body := msg.Content
		if msg.ToolResult != nil {
			body += msg.ToolResult.Content
		}
		n += strings.Count(body, "Code: "+code)
	}
	return n
}

// AC-MID-07, AC-MID-08, AC-MID-09, MC-INV-09: the wait path through the real batch,
// the stock OAR policy and the real doom-loop guard, on one run.
func TestWaitPathOnTheRealStack(t *testing.T) {
	const pendingText = "Uncited progress."
	t.Run("AC-MID-08 a note in the same response lets the wait run", func(t *testing.T) {
		h, sess, _, err := runStack(t, &stackScriptLLM{steps: []stackStep{
			commandStep("", "cat -n notes.txt"),
			waitStep("The first note is on [notes.txt:1]."),
			{text: "The notes hold two lines [notes.txt:2]."},
		}}, "inspect")
		testutil.FailErr(t, "prompt", err)
		msgs := stackMessages(t, h, sess.ID)
		notes := agentNotes(msgs)
		if len(notes) != 1 || codeCount(msgs, "WAIT_HIDDEN_TEXT") != 0 || len(toolResults(msgs, "wait", wire.ToolResultOutcomeRejected)) != 0 {
			t.Fatalf("notes = %d, refused waits = %d", len(notes), codeCount(msgs, "WAIT_HIDDEN_TEXT"))
		}
		waits := toolResults(msgs, "wait", wire.ToolResultOutcomeCompleted)
		if len(waits) != 1 || notes[0].Ord >= waits[0].Ord {
			t.Fatalf("the note must commit before the wait result: waits = %+v, note ord = %d", waits, notes[0].Ord)
		}
	})
	t.Run("AC-MID-07 and AC-MID-09 debt refuses a silent wait once, then the bound admits it", func(t *testing.T) {
		h, sess, _, err := runStack(t, &stackScriptLLM{steps: []stackStep{
			commandStep("", "cat -n notes.txt"),
			waitStep(pendingText),
			waitStep(""),
			{text: "The notes hold two lines [notes.txt:2]."},
		}}, "inspect")
		testutil.FailErr(t, "prompt", err)
		msgs := stackMessages(t, h, sess.ID)
		refused := toolResults(msgs, "wait", wire.ToolResultOutcomeRejected)
		if len(refused) != 1 || !strings.Contains(refused[0].Content, "Code: WAIT_HIDDEN_TEXT") {
			t.Fatalf("refused waits = %+v", refused)
		}
		if got := toolResults(msgs, "wait", wire.ToolResultOutcomeCompleted); len(got) != 1 {
			t.Fatalf("the identical wait within the repair bound did not run: completed = %d, refusals = %+v", len(got), refused)
		}
		for _, note := range agentNotes(msgs) {
			if strings.Contains(note.Content, pendingText) {
				t.Fatalf("withheld text reached the user: %q", note.Content)
			}
		}
	})
}

// AC-MID-15, MC-INV-13: mid-turn and closeout citation defects drain one pool, so
// a run that spent friction before closing gets fewer closeout repairs and still
// ends in the honest host assembly. Lines vary so that repeated-offender
// termination does not end the cycle first.
func TestMidTurnAndCloseoutSpendOneGroundingPool(t *testing.T) {
	closeoutAttempts := func(t *testing.T, midTurn int) int {
		t.Helper()
		script := &stackScriptLLM{steps: []stackStep{commandStep("", "cat -n notes.txt")}}
		for i := range midTurn {
			script.steps = append(script.steps, commandStep("See [read#"+strconv.Itoa(900+i)+":1].", "echo step"))
		}
		script.tail = func(call int) stackStep {
			return stackStep{text: "The notes end at [notes.txt:" + strconv.Itoa(100+call) + "]."}
		}
		h, sess, _, err := runStack(t, script, "inspect")
		testutil.FailErr(t, "prompt", err)
		msgs := stackMessages(t, h, sess.ID)
		var final *wire.Message
		for i := range msgs {
			if msgs[i].Kind == wire.MessageKindCompletionReport {
				final = &msgs[i]
			}
		}
		if final == nil || final.Grounding == nil || !final.Grounding.HostAssembled || final.Grounding.Traced {
			t.Fatalf("exhaustion must end in untraced host assembly: %+v", final)
		}
		if len(agentNotes(msgs)) != 0 {
			t.Fatalf("an invalid citation published: %+v", agentNotes(msgs))
		}
		return script.calls - 1 - midTurn
	}
	unspent := closeoutAttempts(t, 0)
	spent := closeoutAttempts(t, 2)
	if spent >= unspent {
		t.Fatalf("closeout attempts after spending friction mid-turn = %d, without = %d", spent, unspent)
	}
}

// AC-COMPAT-09: bare JSON at run level is no answer. The stock policy names it and
// the next Markdown reply is what the turn accepts.
func TestBareJSONIsNoAnswerOnTheRealStack(t *testing.T) {
	const bare = `{"synthesis":"Done.","cited_evidence":[]}`
	h, sess, _, err := runStack(t, &stackScriptLLM{steps: []stackStep{
		commandStep("", "cat -n notes.txt"),
		{text: bare},
		{text: "The notes hold two lines [notes.txt:2]."},
	}}, "inspect")
	testutil.FailErr(t, "prompt", err)
	msgs := stackMessages(t, h, sess.ID)
	if got := codeCount(msgs, "COORDINATOR_CLOSEOUT_NO_ANSWER"); got != 1 {
		t.Fatalf("COORDINATOR_CLOSEOUT_NO_ANSWER fired %d times", got)
	}
	var final *wire.Message
	for i := range msgs {
		if msgs[i].Kind == wire.MessageKindCompletionReport {
			if strings.Contains(msgs[i].Content, "synthesis") {
				t.Fatalf("bare JSON was accepted: %q", msgs[i].Content)
			}
			final = &msgs[i]
		}
	}
	if final == nil || !strings.Contains(final.Content, "two lines") || final.Grounding == nil || !final.Grounding.Traced || final.Grounding.HostAssembled {
		t.Fatalf("final = %+v", final)
	}
}

// AC-MID-10, MC-INV-16: a turn that dies after its note and its tool leaves one
// note and one tool result. Recovery and the next prompt publish and replay
// nothing, and the text the user received creates no debt.
func TestNoteAndToolResultSurviveAnInterruptedTurnOnce(t *testing.T) {
	script := &stackScriptLLM{steps: []stackStep{
		commandStep("Starting on the notes.", "echo ran"),
		{fail: errors.New("connection lost")},
		{text: "The effect was recorded once."},
	}}
	h, sess, _, firstErr := runStack(t, script, "inspect")
	if firstErr == nil {
		t.Log("the provider failure ended the turn without a prompt error")
	}
	testutil.FailErr(t, "recover", h.Sessions.Manager.Stops.Recovery.RecoverOrphanedTurnsForSession(t.Context(), sess.ID))
	_, err := h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "Keep going")
	testutil.FailErr(t, "continue", err)
	msgs := stackMessages(t, h, sess.ID)
	notes := agentNotes(msgs)
	if len(notes) != 1 || notes[0].Content != "Starting on the notes." || notes[0].ID != wire.MidTurnNoteID(carrierOf(t, msgs, "command")) {
		t.Fatalf("notes = %+v", notes)
	}
	// The program's receipt and its effect each happen once.
	ran := toolResults(msgs, "command", wire.ToolResultOutcomeCompleted)
	if len(ran) != 1 {
		t.Fatalf("command results = %+v", ran)
	}
	if !strings.Contains(ran[0].Content, "ran") {
		t.Fatalf("the command did not run: %q", ran[0].Content)
	}
	if script.calls != 3 {
		t.Fatalf("model calls = %d, want the note and tool, the failure, and the continuation", script.calls)
	}
	if codeCount(msgs, "WAIT_HIDDEN_TEXT") != 0 {
		t.Fatal("published text left debt")
	}
}

func carrierOf(t *testing.T, msgs []wire.Message, tool string) string {
	t.Helper()
	for _, msg := range msgs {
		if msg.Role == wire.MessageRoleAssistant && len(msg.ToolCalls) > 0 && msg.ToolCalls[0].Name == tool {
			return msg.ID
		}
	}
	t.Fatalf("no carrier for %s", tool)
	return ""
}
