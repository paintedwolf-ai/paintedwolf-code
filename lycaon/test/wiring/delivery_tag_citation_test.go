package wiring

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var deliveryTagRE = regexp.MustCompile(`\[(read#[0-9]+) ([^\]\n]+)\]`)

// newestToolContent returns what the provider request carries for the newest result.
func newestToolContent(req modelcall.CompletionRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if msg := req.Messages[i]; msg.Role == wire.MessageRoleTool {
			return msg.Content
		}
	}
	return ""
}

func deliveryTagSession(t *testing.T, script *citableScriptLLM) (*Harness, string) {
	t.Helper()
	t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	h := BuildForTest(t, WithLLMClient(script))
	dir := t.TempDir()
	testutil.FailErr(t, "seed notes", os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("first note\nsecond note\n"), 0o644))
	testutil.FailErr(t, "seed lines", os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("alpha\nbravo\ncharlie\ndelta\n"), 0o644))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, t.Context(), sess.ID)
	return h, sess.ID
}

// One read makes one tag stating what the host delivered, for numbered output and a
// plain section alike. A pipeline maps nothing and keeps its command handle. A citation
// copied from a tag resolves. A coordinate with no excerpt is traced either way.
func TestDeliveryTagsStateCoverageThroughTheRealPath(t *testing.T) {
	var numbered, section, piped, copied string
	script := &citableScriptLLM{steps: []citableStep{
		{command: "cat -n notes.txt"},
		{text: func(req modelcall.CompletionRequest) string {
			numbered = newestToolContent(req)
			return ""
		}, command: "sed -n '2,3p' lines.txt"},
		{text: func(req modelcall.CompletionRequest) string {
			section = newestToolContent(req)
			return ""
		}, command: "cat -n lines.txt | sed -n '2,3p'"},
		{text: func(req modelcall.CompletionRequest) string {
			piped = newestToolContent(req)
			if match := deliveryTagRE.FindStringSubmatch(section); match != nil {
				copied = match[0]
			}
			return "The note is second [notes.txt:2], and the section holds bravo " + copied + "."
		}},
	}}
	h, session := deliveryTagSession(t, script)
	_, err := h.Sessions.Manager.Submissions.Prompt(t.Context(), session, "read the files")
	testutil.FailErr(t, "prompt", err)

	tags := deliveryTagRE.FindAllStringSubmatch(numbered, -1)
	if len(tags) != 1 || tags[0][2] != "notes.txt:1-2" || !strings.Contains(numbered, "second note") {
		t.Fatalf("cat -n tags = %q in %s", tags, numbered)
	}
	if !strings.Contains(numbered, "2\tsecond note") {
		t.Fatalf("stdout changed: %s", numbered)
	}
	tags = deliveryTagRE.FindAllStringSubmatch(section, -1)
	if len(tags) != 1 || tags[0][2] != "lines.txt:2-3" {
		t.Fatalf("sed -n tags = %q in %s", tags, section)
	}
	if deliveryTagRE.MatchString(piped) || !strings.Contains(piped, "command#") || !strings.Contains(piped, "     2\tbravo") {
		t.Fatalf("a pipeline reported coverage or lost its handle: %s", piped)
	}

	msgs, err := h.Sessions.Manager.Runner.Transcript.GetMessages(t.Context(), session)
	testutil.FailErr(t, "read messages", err)
	var last wire.Message
	for _, msg := range msgs {
		if msg.Role == wire.MessageRoleAssistant && len(msg.ToolCalls) == 0 && !wire.IsAgentNoteMessage(msg) {
			last = msg
		}
	}
	g := last.Grounding
	if g == nil || !g.Traced || g.HostAssembled || g.EvidenceOptional {
		t.Fatalf("closeout %q grounding = %+v", last.Content, g)
	}
	verdicts := map[string]string{}
	for _, cited := range g.CitedEvidence {
		verdicts[cited.Path] = string(cited.Verdict)
	}
	if verdicts["notes.txt"] != "traced" || verdicts["lines.txt"] != "traced" {
		t.Fatalf("verdicts = %v, citations %+v", verdicts, g.CitedEvidence)
	}
	if strings.Contains(last.Content, copied) {
		t.Fatalf("the copied tag was not presented as a citation: %q", last.Content)
	}
}

// `cat -n` keeps counting across files, so a later file's printed numbers are not its
// lines. Its tag states the real range, and a coordinate inside that range is refused
// until an excerpt verifies it. The script's second answer is reached only by that refusal.
func TestNumberedReadOfSeveralFilesKeepsLaterFilesUnnumbered(t *testing.T) {
	var printed string
	script := &citableScriptLLM{steps: []citableStep{
		{command: "cat -n notes.txt lines.txt"},
		{text: func(req modelcall.CompletionRequest) string {
			printed = newestToolContent(req)
			return "The section opens with the third printed row [lines.txt:3]."
		}},
		{text: func(modelcall.CompletionRequest) string {
			return `The section holds bravo [lines.txt:2 "bravo"], and the note is second [notes.txt:2].`
		}},
	}}
	h, session := deliveryTagSession(t, script)
	_, err := h.Sessions.Manager.Submissions.Prompt(t.Context(), session, "read the files")
	testutil.FailErr(t, "prompt", err)
	tags := deliveryTagRE.FindAllStringSubmatch(printed, -1)
	if len(tags) != 2 || tags[0][2] != "notes.txt:1-2" || tags[1][2] != "lines.txt:1-4" {
		t.Fatalf("tags = %q in %s", tags, printed)
	}
	if !strings.Contains(printed, "3\talpha") {
		t.Fatalf("stdout changed: %s", printed)
	}
	if script.next != len(script.steps) {
		t.Fatalf("a printed number from a later file was accepted after %d of %d steps", script.next, len(script.steps))
	}
	g := lastAnswerGrounding(t, h, session)
	if g == nil || !g.Traced || g.HostAssembled {
		t.Fatalf("grounding = %+v", g)
	}
	verdicts := map[string]string{}
	for _, cited := range g.CitedEvidence {
		verdicts[fmt.Sprintf("%s:%d", cited.Path, cited.Line)] = string(cited.Verdict)
	}
	if verdicts["lines.txt:2"] != "matched" || verdicts["notes.txt:2"] != "traced" {
		t.Fatalf("verdicts = %v", verdicts)
	}
}

// `grep -n` prints each line's own number beside its path, however many files it reads.
func TestNumberedSearchOfSeveralFilesKeepsItsLines(t *testing.T) {
	script := &citableScriptLLM{steps: []citableStep{
		{command: "grep -n -e second -e bravo notes.txt lines.txt"},
		{text: func(modelcall.CompletionRequest) string {
			return "The note is second [notes.txt:2], and the section holds bravo [lines.txt:2]."
		}},
	}}
	h, session := deliveryTagSession(t, script)
	_, err := h.Sessions.Manager.Submissions.Prompt(t.Context(), session, "search the files")
	testutil.FailErr(t, "prompt", err)
	g := lastAnswerGrounding(t, h, session)
	if script.next != len(script.steps) || g == nil || !g.Traced || g.HostAssembled || len(g.CitedEvidence) != 2 {
		t.Fatalf("grep coordinates: steps %d, grounding %+v", script.next, g)
	}
}

func lastAnswerGrounding(t *testing.T, h *Harness, session string) *wire.CitationGrounding {
	t.Helper()
	msgs, err := h.Sessions.Manager.Runner.Transcript.GetMessages(t.Context(), session)
	testutil.FailErr(t, "read messages", err)
	var last wire.Message
	for _, msg := range msgs {
		if msg.Role == wire.MessageRoleAssistant && len(msg.ToolCalls) == 0 && !wire.IsAgentNoteMessage(msg) {
			last = msg
		}
	}
	return last.Grounding
}

// A coordinate on what only a pipeline printed is refused, and an excerpt on the
// command then grounds the repaired answer.
func TestPipelineCoordinateRepairsThroughACommandExcerpt(t *testing.T) {
	script := &citableScriptLLM{steps: []citableStep{
		{command: "cat -n lines.txt | sed -n '2,3p'"},
		{text: func(modelcall.CompletionRequest) string { return "It holds bravo [lines.txt:2]." }},
		{text: func(modelcall.CompletionRequest) string { return "It holds bravo [command#1 \"bravo\"]." }},
	}}
	h, session := deliveryTagSession(t, script)
	_, err := h.Sessions.Manager.Submissions.Prompt(t.Context(), session, "read the file")
	testutil.FailErr(t, "prompt", err)
	msgs, err := h.Sessions.Manager.Runner.Transcript.GetMessages(t.Context(), session)
	testutil.FailErr(t, "read messages", err)
	var last wire.Message
	for _, msg := range msgs {
		if msg.Role == wire.MessageRoleAssistant && len(msg.ToolCalls) == 0 && !wire.IsAgentNoteMessage(msg) {
			last = msg
		}
	}
	if last.Grounding == nil || !last.Grounding.Traced || last.Grounding.HostAssembled {
		t.Fatalf("the excerpt on the command did not ground: %q %+v", last.Content, last.Grounding)
	}
}

// A host operation that returns source windows records them from its own result, so a
// window cites by its handle and by path. Operations that return no source text keep
// only their command handle.
func TestProgramOperationWindowsAreCitableThroughTheRealPath(t *testing.T) {
	t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	const source = "package audit\n\n// Alpha adds one.\nfunc Alpha(n int) int {\n\treturn n + 1\n}\n"
	var windowResult, outlineResult, repoResult, surveyResult string
	var handle, path, lines string
	script := &citableScriptLLM{steps: []citableStep{
		{command: "summarize --path audit.go --focus Alpha"},
		{text: func(req modelcall.CompletionRequest) string {
			windowResult = newestToolContent(req)
			return ""
		}, command: "outline --path audit.go"},
		{text: func(req modelcall.CompletionRequest) string {
			outlineResult = newestToolContent(req)
			return ""
		}, command: "repo-map --path . --depth 1"},
		{text: func(req modelcall.CompletionRequest) string {
			repoResult = newestToolContent(req)
			return ""
		}, command: "survey-repo --path . --depth 1"},
		{text: func(req modelcall.CompletionRequest) string {
			surveyResult = newestToolContent(req)
			if match := deliveryTagRE.FindStringSubmatch(windowResult); match != nil {
				handle = match[1]
				path, lines, _ = strings.Cut(match[2], ":")
			}
			first, _, _ := strings.Cut(lines, "-")
			return "Alpha is in the window [" + handle + ":" + first + "], which also cites by path [" + path + ":" + first + "]."
		}},
	}}
	h := BuildForTest(t, WithLLMClient(script))
	dir := t.TempDir()
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(dir, "audit.go"), []byte(source), 0o644))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, t.Context(), sess.ID)
	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "read the file")
	testutil.FailErr(t, "prompt", err)

	if handle == "" || path != "audit.go" {
		t.Fatalf("summarize gave no window tag: %s", windowResult)
	}
	for name, result := range map[string]string{"outline": outlineResult, "repo-map": repoResult, "survey-repo": surveyResult} {
		if deliveryTagRE.MatchString(result) || !strings.Contains(result, "command#") {
			t.Fatalf("%s should hold only its command handle: %.300s", name, result)
		}
	}
	msgs, err := h.Sessions.Manager.Runner.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read messages", err)
	var last wire.Message
	for _, msg := range msgs {
		if msg.Role == wire.MessageRoleAssistant && len(msg.ToolCalls) == 0 && !wire.IsAgentNoteMessage(msg) {
			last = msg
		}
	}
	g := last.Grounding
	if g == nil || !g.Traced || g.HostAssembled || g.HintCode != "" || len(g.CitedEvidence) == 0 {
		t.Fatalf("closeout %q grounding = %+v", last.Content, g)
	}
	for _, cited := range g.CitedEvidence {
		if cited.Path != path || cited.Handle != handle {
			t.Fatalf("citation %+v, want %s on %s", cited, handle, path)
		}
	}
}
