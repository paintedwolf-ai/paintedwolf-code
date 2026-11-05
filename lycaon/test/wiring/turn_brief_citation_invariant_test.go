package wiring

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// briefReadingLLM answers from what each request actually carries, as a model
// holding only its prompt would.
type briefReadingLLM struct {
	mu    sync.Mutex
	steps []func(modelcall.CompletionRequest) stackStep
	calls int
}

func (m *briefReadingLLM) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if len(req.Tools) == 0 {
		return &modelcall.Completion{Content: "Chat."}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	step := stackStep{text: "Finished."}
	if len(m.steps) > 0 {
		step = m.steps[0](req)
		m.steps = m.steps[1:]
	}
	out := &modelcall.Completion{Content: step.text}
	if step.tool != "" {
		out.ToolCalls = []wire.ToolCall{{ID: "call-" + strconv.Itoa(m.calls), Name: step.tool, Args: step.args}}
	}
	return out, nil
}

func (m *briefReadingLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	out, err := m.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: out.Content, ToolCalls: out.ToolCalls, Done: true}
	close(ch)
	return ch, nil
}

// promptedRowHandles parses host context the way the citation grammar reads it.
func promptedRowHandles(req modelcall.CompletionRequest) []string {
	var out []string
	for _, msg := range req.Messages {
		if msg.Role != wire.MessageRoleSystem {
			continue
		}
		for _, occurrence := range citationmd.Parse(msg.Content, citationmd.Options{}).Occurrences {
			if occurrence.Kind != citationmd.KindCitation {
				continue
			}
			if kind, _ := evidence.ParseHandleOrdinal(occurrence.Citation.Handle); kind == evidence.KindSourceChange {
				out = append(out, occurrence.Citation.Handle)
			}
		}
	}
	return out
}

// postTurnFeedback lists the transcript positions of feedback a post-turn check produced.
func postTurnFeedback(t *testing.T, msgs []wire.Message) []int {
	t.Helper()
	var out []int
	for i, msg := range msgs {
		for _, part := range msg.ContentParts {
			if part.Source != "policy_feedback" {
				continue
			}
			var entry guidance.PolicyFeedback
			testutil.FailErr(t, "decode policy feedback", json.Unmarshal([]byte(part.Content), &entry))
			if entry.Anchor == oar.AnchorCoordinatorPostTurn {
				out = append(out, i)
			}
		}
	}
	return out
}

func lastCompletion(t *testing.T, msgs []wire.Message) wire.Message {
	t.Helper()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Kind == wire.MessageKindCompletionReport {
			return msgs[i]
		}
	}
	t.Fatal("no completion report")
	return wire.Message{}
}

func messagesOfRole(msgs []wire.Message, role wire.MessageRole) int {
	n := 0
	for _, msg := range msgs {
		if msg.Role == role {
			n++
		}
	}
	return n
}

// A session's turn-start brief, end to end on the real stack: whatever the host
// states is citable with no tool call, the citation grounds the answer, a brief
// creates no duty on a turn that does not use it, and feedback on an ended turn
// never lands under the next user's question.
func TestTurnBriefRowsGroundAnAnswerOnTheRealStack(t *testing.T) {
	t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	var citedSecond []string
	script := &briefReadingLLM{steps: []func(modelcall.CompletionRequest) stackStep{
		func(modelcall.CompletionRequest) stackStep { return commandStep("", "cat -n notes.txt") },
		func(modelcall.CompletionRequest) stackStep { return stackStep{text: "Two notes."} },
		func(req modelcall.CompletionRequest) stackStep {
			citedSecond = promptedRowHandles(req)
			text := "notes.txt was changed"
			for _, handle := range citedSecond {
				text += " [" + handle + "]"
			}
			return stackStep{text: text + "."}
		},
		func(modelcall.CompletionRequest) stackStep { return stackStep{text: "Noted."} },
	}}
	h := BuildForTest(t, WithLLMClient(script))
	ctx := t.Context()
	dir := h.ProjectDir(t, "project")
	const before = "first note\nsecond note\n"
	testutil.FailErr(t, "seed notes", os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(before), 0o644))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	manager := h.Sessions.Manager
	invocation, err := manager.ToolContext.Build(ctx, sess, "implement", inject.Machine{})
	testutil.FailErr(t, "build source invocation", err)
	source := invocation.Source
	outsideEdit := func(from, to string) {
		t.Helper()
		testutil.FailErr(t, "record outside edit", source.SourceLedger.Record(ctx, sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: source.ActiveRootID, Path: "notes.txt"},
			ProjectID:      sess.ProjectID,
			BranchID:       source.ProjectSourceBranch, Op: wire.SourceChangeOpWrite,
			Origin: wire.SourceChangeOriginExternal, Before: []byte(from), After: []byte(to),
		}))
	}

	_, err = manager.Submissions.Prompt(ctx, sess.ID, "read the notes")
	testutil.FailErr(t, "first turn", err)
	first := stackMessages(t, h, sess.ID)
	if audit := lastCompletion(t, first).Grounding; audit == nil || audit.Traced || !audit.HostAssembled {
		t.Fatalf("the first answer must leave an evidence gap the host settled for this case: %+v", audit)
	}

	outsideEdit(before, "changed once\n")
	_, err = manager.Submissions.Prompt(ctx, sess.ID, "which files changed?")
	testutil.FailErr(t, "second turn", err)
	second := stackMessages(t, h, sess.ID)
	if len(citedSecond) == 0 {
		t.Fatal("the second turn's prompt offered no citable row")
	}
	tools := messagesOfRole(second, wire.MessageRoleTool) - messagesOfRole(first, wire.MessageRoleTool)
	answer := lastCompletion(t, second)
	if tools != 0 || answer.Grounding == nil || !answer.Grounding.Traced || answer.Grounding.HostAssembled || answer.Grounding.EvidenceOptional {
		t.Fatalf("tool messages = %d grounding = %+v, want a traced answer from the brief alone", tools, answer.Grounding)
	}
	var grounded []string
	for _, cited := range answer.Grounding.CitedEvidence {
		grounded = append(grounded, cited.Handle)
	}
	if !slices.Equal(grounded, citedSecond) {
		t.Fatalf("grounded on %v, cited %v", grounded, citedSecond)
	}

	outsideEdit("changed once\n", "changed twice\n")
	_, err = manager.Submissions.Prompt(ctx, sess.ID, "thanks")
	testutil.FailErr(t, "third turn", err)
	third := stackMessages(t, h, sess.ID)
	if audit := lastCompletion(t, third).Grounding; audit == nil || !audit.EvidenceOptional || audit.HostAssembled {
		t.Fatalf("a brief must create no citation duty: %+v", audit)
	}

	t.Run("a gap the host settled leaves no post-turn feedback", func(t *testing.T) {
		// The host settled the first turn's gap, so nothing remains to ask of later turns.
		if positions := postTurnFeedback(t, third); len(positions) != 0 {
			t.Fatalf("a settled gap produced post-turn feedback at %v", positions)
		}
	})

	t.Run("every row printed has a record and every record a row", func(t *testing.T) {
		sessNow, err := manager.Chats.Get(ctx, sess.ID)
		testutil.FailErr(t, "reload session", err)
		var rows []string
		for _, brief := range manager.SourceBriefs.Recorded(ctx, sessNow) {
			for _, row := range brief.Files {
				rows = append(rows, row.Handle)
			}
			for _, row := range brief.Git {
				rows = append(rows, row.Handle)
			}
		}
		ledger, err := h.Store.LoadLedger(ctx, sess.ID)
		testutil.FailErr(t, "load ledger", err)
		var records []string
		for handle, rec := range ledger.Handles {
			if rec.Kind == evidence.KindSourceChange {
				records = append(records, handle)
			}
		}
		slices.Sort(rows)
		slices.Sort(records)
		if len(records) < 2 || !slices.Equal(rows, slices.Compact(slices.Clone(rows))) || !slices.Equal(rows, records) {
			t.Fatalf("rows = %v, records = %v", rows, records)
		}
		for path, handles := range ledger.ByPath {
			for _, handle := range handles {
				if strings.HasPrefix(handle, evidence.KindSourceChange+"#") {
					t.Fatalf("%s entered the path index under %s", handle, path)
				}
			}
		}
	})
}
