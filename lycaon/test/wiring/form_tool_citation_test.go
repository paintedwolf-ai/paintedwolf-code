package wiring

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const formToolSource = "package audit\n\n// Alpha adds one.\nfunc Alpha(n int) int {\n\treturn n + 1\n}\n\n// Beta is unrelated.\nfunc Beta() {}\n\n// Gamma calls Alpha.\nfunc Gamma() int {\n\treturn Alpha(1)\n}\n"

// runFormToolCitation calls one step, then cites the window it delivered.
func runFormToolCitation(t *testing.T, step citableStep) (result string, tags [][]string, closeout wire.Message, ledgerKinds map[string]int) {
	t.Helper()
	t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	script := &citableScriptLLM{steps: []citableStep{step, {text: func(req modelcall.CompletionRequest) string {
		result = newestToolContent(req)
		tags = deliveryTagRE.FindAllStringSubmatch(result, -1)
		if len(tags) == 0 {
			return "No window was delivered."
		}
		path, lines, _ := strings.Cut(tags[0][2], ":")
		first, _, _ := strings.Cut(lines, "-")
		return "Alpha is in the window [" + path + ":" + first + "]."
	}}}}
	h := BuildForTest(t, WithLLMClient(script))
	dir := t.TempDir()
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(dir, "audit.go"), []byte(formToolSource), 0o644))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, t.Context(), sess.ID)
	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "explain the file")
	testutil.FailErr(t, "prompt", err)
	msgs, err := h.Sessions.Manager.Runner.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read messages", err)
	for _, msg := range msgs {
		if msg.Role == wire.MessageRoleAssistant && len(msg.ToolCalls) == 0 && !wire.IsAgentNoteMessage(msg) {
			closeout = msg
		}
	}
	ledger, err := h.Store.LoadLedger(t.Context(), sess.ID)
	testutil.FailErr(t, "load ledger", err)
	ledgerKinds = map[string]int{}
	for _, record := range ledger.Handles {
		ledgerKinds[string(record.Kind)]++
	}
	return result, tags, closeout, ledgerKinds
}

// One call through the tool records what the same call through command records,
// once, and a coordinate from it is traced without an excerpt.
func TestSummarizeToolRecordsOneCitableSetOfWindows(t *testing.T) {
	_, viaCommand, _, commandKinds := runFormToolCitation(t, citableStep{command: "summarize --path audit.go --focus Alpha"})
	result, viaTool, closeout, toolKinds := runFormToolCitation(t, citableStep{tool: "summarize", args: map[string]any{"path": "audit.go", "task": "Alpha"}})
	if len(viaTool) == 0 {
		t.Fatalf("the tool delivered no window: %.600s", result)
	}
	if len(viaTool) != len(viaCommand) {
		t.Fatalf("tool recorded %d windows, command %d: %v vs %v", len(viaTool), len(viaCommand), viaTool, viaCommand)
	}
	for i := range viaTool {
		if viaTool[i][2] != viaCommand[i][2] {
			t.Fatalf("window %d = %s, command %s", i, viaTool[i][2], viaCommand[i][2])
		}
	}
	if !maps.Equal(toolKinds, commandKinds) {
		t.Fatalf("tool ledger %v, command ledger %v", toolKinds, commandKinds)
	}
	grounding := closeout.Grounding
	if grounding == nil || !grounding.Traced || grounding.HostAssembled || len(grounding.CitedEvidence) == 0 {
		t.Fatalf("closeout %q grounding = %+v", closeout.Content, grounding)
	}
	for _, cited := range grounding.CitedEvidence {
		if cited.Path != "audit.go" || string(cited.Verdict) != "traced" || strings.HasPrefix(cited.Handle, "summarize#") {
			t.Fatalf("citation = %+v", cited)
		}
	}
}

// Separate pattern hits must not lose their citation for being apart.
func TestSummarizePatternHitsStayCitable(t *testing.T) {
	result, tags, closeout, _ := runFormToolCitation(t, citableStep{tool: "summarize", args: map[string]any{"path": "audit.go", "pattern": "Alpha", "task": "Alpha"}})
	if len(tags) == 0 {
		t.Fatalf("pattern hits delivered no citable window: %.900s", result)
	}
	if g := closeout.Grounding; g == nil || !g.Traced || g.HostAssembled {
		t.Fatalf("closeout %q grounding = %+v", closeout.Content, g)
	}
}
