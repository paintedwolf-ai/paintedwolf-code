package wiring

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var observedHandlesRE = regexp.MustCompile(`Observed handles: ([^\s,.]+)`)

// Every command prints a handle, so text after one needs a citation, and what the
// notice names resolves. A new request starts with nothing to cite again, even when an
// earlier turn left evidence in the ledger.
func TestMidTurnRequirementFollowsWhatTheModelCanCite(t *testing.T) {
	t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	var observed string
	script := &CitableScriptLLM{Steps: []CitableStep{
		{Command: "cat -n notes.txt"},
		{Text: func(modelcall.CompletionRequest) string { return "Uncited after a file was delivered." }, Command: "echo one"},
		{Text: func(req modelcall.CompletionRequest) string {
			for _, msg := range req.Messages {
				if match := observedHandlesRE.FindStringSubmatch(msg.Content); match != nil && strings.Contains(msg.Content, "MID_TURN_NOTE_UNGROUNDED") {
					observed = match[1]
				}
			}
			return "Cited by the handle the notice named [" + observed + "], and by path [notes.txt:2]."
		}, Command: "echo two"},
		{Text: func(modelcall.CompletionRequest) string { return "The notes hold two lines [notes.txt:1]." }},
		{Text: func(modelcall.CompletionRequest) string { return "Nothing here to cite yet." }},
	}}
	h := BuildForTest(t, WithLLMClient(script))
	dir := t.TempDir()
	testutil.FailErr(t, "seed notes", os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("first note\nsecond note\n"), 0o644))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, t.Context(), sess.ID)

	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "first request")
	testutil.FailErr(t, "first prompt", err)
	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "second request")
	testutil.FailErr(t, "second prompt", err)

	msgs, err := h.Sessions.Manager.Runner.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read messages", err)
	var notes []string
	var last wire.Message
	for _, msg := range msgs {
		if wire.IsAgentNoteMessage(msg) {
			notes = append(notes, msg.Content)
		}
		if msg.Role == wire.MessageRoleAssistant && len(msg.ToolCalls) == 0 && !wire.IsAgentNoteMessage(msg) {
			last = msg
		}
	}
	if observed == "" {
		t.Fatalf("the withheld notice named no handle; notes = %q", notes)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %q, want the cited note", notes)
	}
	if strings.Contains(notes[0], "Uncited") || !strings.Contains(notes[0], "notes.txt#L2") {
		t.Fatalf("cited note = %q", notes[0])
	}
	if last.Content != "Nothing here to cite yet." || last.Grounding == nil || !last.Grounding.EvidenceOptional || last.Grounding.HostAssembled {
		t.Fatalf("closeout with nothing to cite = %q grounding %+v", last.Content, last.Grounding)
	}
}
