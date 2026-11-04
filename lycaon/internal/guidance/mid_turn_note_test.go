package guidance_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

const midTurnArtifact = "11111111-1111-4111-8111-111111111111"

// midTurnLedger holds one read, one accepted dispatch, one survey and one
// command receipt, as a coordinator's ledger would.
func midTurnLedger() guidance.CloseoutEvidence {
	return guidance.CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#3", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", LineRanges: []evidence.LineRange{{Start: 40, End: 50}}, Body: []string{"line"}},
		{Handle: "task#1", Kind: "task", Shape: evidence.ShapeStructuredEvent, Body: []string{`{"job_id":"w1","status":"enqueued"}`}},
		{Handle: "list#2", Kind: "list", Shape: evidence.ShapeFileRegion, Survey: true, Path: "internal"},
		{Handle: "command#4", Kind: "command", Shape: evidence.ShapeCommand, Body: []string{"ok"}},
	})}
}

func TestAuditMidTurnNote(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("Worker one finished its survey. ", 40) + "\n\n" + strings.Repeat("Worker two is still running. ", 40)
	empty := guidance.CloseoutEvidence{Ledger: evidence.InitLedger()}
	for _, tc := range []struct {
		name        string
		text        string
		ev          guidance.CloseoutEvidence
		required    bool
		presentable []string
		want        guidance.MidTurnOutcome
		code        string
		friction    bool
		contains    string
		absent      string
		traced      bool
		optional    bool
	}{
		{name: "AC-MID-01 pre-evidence update publishes as evidence-optional", text: "I'll inspect the parser.", ev: empty, want: guidance.MidTurnPublish, contains: "inspect the parser", traced: true, optional: true},
		{name: "AC-MID-02 pending handle is rejected even before evidence", text: "Started [task#1].", ev: empty, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteHandleUngroundCode, friction: true},
		{name: "AC-MID-03 cited update publishes without its marker", text: "The timeout is set in [read#3:42]. Checking callers.", ev: midTurnLedger(), required: true, want: guidance.MidTurnPublish, contains: "Checking callers", absent: "read#3", traced: true},
		{name: "AC-MID-04 uncited after observations withholds", text: "Still looking.", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteUngroundedCode},
		{name: "AC-MID-05 invalid citation withholds and charges", text: "See [read#99:1].", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteHandleUngroundCode, friction: true},
		{name: "AC-MID-05 uncovered range withholds", text: "See [read#3:42-99].", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteHandleUngroundCode, friction: true},
		{name: "AC-MID-06 dispatch receipt after reads, no length cap", text: "Dispatched the worker [task#1].\n\n" + long, ev: midTurnLedger(), required: true, want: guidance.MidTurnPublish, contains: "Worker two", absent: "task#1", traced: true},
		{name: "AC-MID-06 survey handle keeps existence semantics", text: "The package exists [list#2].", ev: midTurnLedger(), required: true, want: guidance.MidTurnPublish, traced: true},
		{name: "AC-MID-06 command receipt", text: "Tests ran [command#4].", ev: midTurnLedger(), required: true, want: guidance.MidTurnPublish, traced: true},
		{name: "AC-MID-12 unknown artifact withholds without friction", text: "Screenshot [read#3:42] [artifact:" + midTurnArtifact + "].", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteArtifactCode},
		{name: "presentable artifact publishes with its citation", text: "Screenshot [read#3:42] [artifact:" + midTurnArtifact + "].", ev: midTurnLedger(), required: true, presentable: []string{midTurnArtifact}, want: guidance.MidTurnPublish, traced: true},
		{name: "artifact alone does not satisfy a citation requirement", text: "[artifact:" + midTurnArtifact + "]", ev: midTurnLedger(), required: true, presentable: []string{midTurnArtifact}, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteUngroundedCode},
		{name: "artifact alone publishes before evidence", text: "[artifact:" + midTurnArtifact + "]", ev: empty, presentable: []string{midTurnArtifact}, want: guidance.MidTurnPublish, traced: true, optional: true},
		{name: "malformed node withholds without friction", text: "Range [read#3:9-2] is odd.", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteSyntaxCode},
		{name: "repair directive has no meaning here", text: "Fixed [cite: c1 => read#3:42].", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteSyntaxCode},
		{name: "embedded artifact image withholds", text: "Look [read#3:42] ![shot](" + midTurnArtifact + ").", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteEmbedCode},
		{name: "literal embed example stays allowed", text: "Syntax is `![shot](" + midTurnArtifact + ")` [read#3:42].", ev: midTurnLedger(), required: true, want: guidance.MidTurnPublish, contains: "![shot]", traced: true},
		{name: "literal citation example is not a citation", text: "The form is `[read#99]`.", ev: empty, want: guidance.MidTurnPublish, contains: "[read#99]", traced: true, optional: true},
		{name: "pathless citation alone says nothing", text: "[task#1]", ev: midTurnLedger(), required: true, want: guidance.MidTurnSilent},
		{name: "a dispatch receipt has no line to cite", text: "Started [task#1:1].", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteHandleUngroundCode, friction: true},
		{name: "a command receipt has no line to cite", text: "Ran [command#4:2].", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteHandleUngroundCode, friction: true},
		{name: "a survey record has no line to cite", text: "Exists [list#2:3].", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteHandleUngroundCode, friction: true},
		{name: "a line the read did not cover", text: "See [read#3:999].", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteHandleUngroundCode, friction: true},
		{name: "an unregistered kind is prose, not an invalid citation", text: "Linked to [pr#123] [read#3:42].", ev: midTurnLedger(), required: true, want: guidance.MidTurnPublish, contains: "[pr#123]", traced: true},
		{name: "an unregistered kind alone is uncited, not invalid", text: "Linked to [pr#123].", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteUngroundedCode},
		{name: "prose brackets are not attempts", text: "Use [and/or] as needed [read#3:42].", ev: midTurnLedger(), required: true, want: guidance.MidTurnPublish, contains: "[and/or]", traced: true},
		{name: "AC-MID-12 unobserved image withholds without friction", text: "Screenshot [read#3:42] ![shot](https://placeholder.example/1.png).", ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteImageCode},
		{name: "unobserved image withholds before evidence too", text: "Plan ![shot](https://placeholder.example/1.png).", ev: empty, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteImageCode},
		{name: "literal image example stays allowed", text: "Syntax is `![shot](https://placeholder.example/1.png)` [read#3:42].", ev: midTurnLedger(), required: true, want: guidance.MidTurnPublish, contains: "![shot]", traced: true},
		{name: "bare retired tool JSON is not an update", text: `{"name":"surface_note","arguments":{"summary":"Done."}}`, ev: empty, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteSyntaxCode},
		{name: "bare report JSON is not an update", text: `{"synthesis":"Done."}`, ev: midTurnLedger(), required: true, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteSyntaxCode},
		{name: "fenced JSON is literal text", text: "```json\n{\"a\":1}\n```", ev: empty, want: guidance.MidTurnPublish, contains: "{\"a\":1}", traced: true, optional: true},
		{name: "unobserved URL before evidence", text: "See [RFC](https://example.test/spec).", ev: empty, want: guidance.MidTurnWithhold, code: guidance.MidTurnNoteURLOnlyCode, friction: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := guidance.AuditMidTurnNote(guidance.MidTurnNoteInput{
				Text: tc.text, SurfaceID: "implement_routing", Evidence: tc.ev,
				CitationsRequired: tc.required, Presentable: tc.presentable, RecentHandles: []string{"task#1"},
			})
			if got.Outcome != tc.want || got.Code != tc.code {
				t.Fatalf("audit = %+v, want %s %q", got, tc.want, tc.code)
			}
			if guidance.MidTurnNoteChargesFriction(got.Code) != tc.friction {
				t.Fatalf("friction for %q, want %v", got.Code, tc.friction)
			}
			if tc.contains != "" && !strings.Contains(got.Content, tc.contains) {
				t.Fatalf("content %q lacks %q", got.Content, tc.contains)
			}
			if tc.absent != "" && strings.Contains(got.Content, tc.absent) {
				t.Fatalf("content %q still shows %q", got.Content, tc.absent)
			}
			if tc.want != guidance.MidTurnPublish {
				return
			}
			if got.Grounding == nil || got.Grounding.Traced != tc.traced || got.Grounding.EvidenceOptional != tc.optional {
				t.Fatalf("grounding = %+v, want traced=%v optional=%v", got.Grounding, tc.traced, tc.optional)
			}
		})
	}
}

// A withheld notice must offer something citable, or recovery is impossible.
func TestMidTurnWithheldDataLeadsWithThisRequestsHandles(t *testing.T) {
	t.Parallel()
	got := guidance.AuditMidTurnNote(guidance.MidTurnNoteInput{
		Text: "Waiting on the worker.", Evidence: midTurnLedger(), CitationsRequired: true, RecentHandles: []string{"task#1", "read#3"},
	})
	if sample, _ := got.Data["observed_handles_sample"].(string); !strings.HasPrefix(sample, "task#1") {
		t.Fatalf("handle sample = %q", sample)
	}
}

func TestIntentWindowObservations(t *testing.T) {
	t.Parallel()
	user := api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "go"}
	read := api.Message{Role: api.MessageRoleTool, EvidenceHandles: []string{"read#1"}, ToolResult: &api.ToolResult{Tool: "read"}}
	dispatch := api.Message{Role: api.MessageRoleTool, EvidenceHandles: []string{"task#1"}, ToolResult: &api.ToolResult{Tool: "task", Dispatch: &api.WorkerDispatch{WorkerID: "w1"}}}
	output := []api.CommandProgramOutputWindow{{Stream: "stdout", Ref: "out-1"}}
	command := api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "command", CommandProgram: &api.CommandProgramReceipt{Outputs: output}}}
	delivered := evidence.AssembleLedger([]evidence.Record{{Handle: "read#7", Kind: "read", Path: "a.go", SourceDelivery: &evidence.SourceDelivery{DeliveryID: "out-1:0:9"}}})
	otherOutput := evidence.AssembleLedger([]evidence.Record{{Handle: "read#7", Kind: "read", Path: "a.go", SourceDelivery: &evidence.SourceDelivery{DeliveryID: "out-2:0:9"}}})
	control := api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "update_progress"}}
	for _, tc := range []struct {
		name    string
		history []api.Message
		ledger  evidence.Ledger
		want    bool
	}{
		{"no tools", []api.Message{user}, evidence.Ledger{}, false},
		{"control tool only", []api.Message{user, control}, evidence.Ledger{}, false},
		{"earlier window does not count", []api.Message{read, user}, evidence.Ledger{}, false},
		{"handle", []api.Message{user, read}, evidence.Ledger{}, true},
		{"dispatch with its printed handle", []api.Message{user, dispatch}, evidence.Ledger{}, true},
		{"command with no file output leaves nothing to cite", []api.Message{user, command}, evidence.Ledger{}, false},
		{"another command's delivery does not count", []api.Message{user, command}, otherOutput, false},
		{"command that delivered source is citable by path", []api.Message{user, command}, delivered, true},
	} {
		if got := guidance.IntentWindowHasCitableObservations(tc.history, tc.ledger); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
	handles := guidance.IntentWindowHandles([]api.Message{read, user, {Role: api.MessageRoleTool, EvidenceHandles: []string{"read#2"}}, {Role: api.MessageRoleTool, EvidenceHandles: []string{"task#1"}}}, 1)
	if len(handles) != 1 || handles[0] != "task#1" {
		t.Fatalf("handles = %v", handles)
	}
}

func TestIntentWindowWorkerFactsReadOnlyStructuredRows(t *testing.T) {
	t.Parallel()
	history := []api.Message{
		{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "go"},
		{Role: api.MessageRoleAssistant, Content: "All workers succeeded."},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "task", Dispatch: &api.WorkerDispatch{WorkerID: "w1", AgentType: "explore"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "task", Dispatch: &api.WorkerDispatch{WorkerID: "w2", AgentType: "implement"}}},
		{Role: api.MessageRoleUser, Visibility: api.MessageVisibilityInternal, WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "w1", AgentType: "explore", Status: "completed"}},
	}
	got := guidance.IntentWindowWorkerFacts(history)
	if len(got) != 2 || got[0] != (guidance.MidTurnWorkerFact{AgentType: "explore", Status: "completed"}) || got[1].Status != "started" {
		t.Fatalf("facts = %+v", got)
	}
}

func TestMidTurnTextHasSpeech(t *testing.T) {
	t.Parallel()
	for text, want := range map[string]bool{
		"":                            false,
		"[read#3]":                    false,
		"[verify: targeted - ran it]": false,
		"Working on it.":              true,
		"`[read#3]`":                  true,
		"[task#1]":                    false,
		"[pr#123]":                    true,
	} {
		if got := guidance.MidTurnTextHasSpeech(text); got != want {
			t.Errorf("%q: got %v", text, got)
		}
	}
}
