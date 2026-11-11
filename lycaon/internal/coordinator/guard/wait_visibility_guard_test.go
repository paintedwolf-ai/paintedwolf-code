package guard

import (
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWaitVisibilityDerivesDebtFromDurableRows(t *testing.T) {
	user := api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Continue"}
	carrier := func(text string) api.Message {
		return api.Message{ID: text + "-id", Role: api.MessageRoleAssistant, Content: text, ToolCalls: []api.ToolCall{{ID: "wait-1", Name: "wait"}}}
	}
	note := api.Message{Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Visible status"}
	refused := api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "wait", Codes: []string{WaitHiddenTextCode}}}
	otherRefusal := api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "wait", Codes: []string{"WAIT_CONDITION_NOT_ALLOWED"}}}
	retracted := carrier("Retracted")
	retracted.DraftStatus = api.DraftStatusRejected
	finalReply := api.Message{Role: api.MessageRoleAssistant, Content: "Reply without tools"}
	for _, tc := range []struct {
		name      string
		history   []api.Message
		publishes bool
		worker    bool
		tool      string
		blocked   bool
	}{
		{name: "AC-MID-04 unpublished text is owed", history: []api.Message{carrier("Status")}, publishes: true, tool: "wait", blocked: true},
		{name: "sibling tool result keeps the debt", history: []api.Message{carrier("Status"), {Role: api.MessageRoleTool, Content: "ok"}}, publishes: true, tool: "wait", blocked: true},
		{name: "AC-MID-13 silent step owes nothing", history: []api.Message{carrier(" \n")}, publishes: true, tool: "wait"},
		{name: "AC-MID-13 parroted header owes nothing", history: []api.Message{carrier("⟦D:model:internal_step_note⟧\n⟦D⟧")}, publishes: true, tool: "wait"},
		{name: "citation-only step says nothing", history: []api.Message{carrier("[read#3]")}, publishes: true, tool: "wait"},
		{name: "AC-MID-08 current note settles the wait", history: []api.Message{carrier("Status"), note}, publishes: true, tool: "wait"},
		{name: "a later note settles earlier deductions", history: []api.Message{carrier("One"), carrier("Two"), note}, publishes: true, tool: "wait"},
		{name: "AC-MID-07 empty text does not clear old debt", history: []api.Message{carrier("Status"), carrier("")}, publishes: true, tool: "wait", blocked: true},
		{name: "text after the latest note is owed", history: []api.Message{note, carrier("Status")}, publishes: true, tool: "wait", blocked: true},
		{name: "a new intent starts clean", history: []api.Message{carrier("Status"), user}, publishes: true, tool: "wait"},
		{name: "a retracted attempt is not speech", history: []api.Message{retracted}, publishes: true, tool: "wait"},
		{name: "a tool-free reply is not a carrier", history: []api.Message{finalReply}, publishes: true, tool: "wait"},
		{name: "AC-MID-19 private surface owes nothing", history: []api.Message{carrier("Status")}, tool: "wait"},
		{name: "AC-MID-14 worker owes nothing", history: []api.Message{carrier("Status")}, publishes: true, worker: true, tool: "wait"},
		{name: "ordinary call is not checked", history: []api.Message{carrier("Status")}, publishes: true, tool: "read"},
		{name: "no history", publishes: true, tool: "wait"},
		{name: "AC-MID-09 exhausted repair stops refusing", history: []api.Message{carrier("Status"), refused}, publishes: true, tool: "wait"},
		{name: "AC-MID-09 new text does not restart repair", history: []api.Message{carrier("Status"), refused, carrier("Changed")}, publishes: true, tool: "wait"},
		{name: "an unrelated refusal spends nothing", history: []api.Message{carrier("Status"), otherRefusal, otherRefusal}, publishes: true, tool: "wait", blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &api.Session{ID: "coordinator"}
			if tc.worker {
				sess.ParentSessionID = "parent"
			}
			gc := oar.NewGuardContext()
			ObserveWaitVisibility(sess, tc.history, tc.tool, tc.publishes, gc)
			if got := hasRejectCode(gc, WaitHiddenTextCode); got != tc.blocked {
				t.Fatalf("blocked=%v, want %v: %+v", got, tc.blocked, gc.Session)
			}
		})
	}
}

func TestWaitVisibilityResetsRepairAfterAnyNote(t *testing.T) {
	carrier := api.Message{Role: api.MessageRoleAssistant, Content: "Status", ToolCalls: []api.ToolCall{{ID: "w", Name: "wait"}}}
	refused := api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "wait", Codes: []string{WaitHiddenTextCode}}}
	note := api.Message{Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Visible"}
	got := DeriveWaitVisibility([]api.Message{carrier, refused, carrier, refused, note, carrier})
	if !got.Pending() || got.RefusedWaits != 0 {
		t.Fatalf("visibility = %+v", got)
	}
}

func TestFallbackSettlesPerDebt(t *testing.T) {
	carrier := func(text string) api.Message {
		return api.Message{Role: api.MessageRoleAssistant, Content: text, ToolCalls: []api.ToolCall{{ID: "w", Name: "wait"}}}
	}
	refused := api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "wait", Codes: []string{WaitHiddenTextCode}}}
	ran := api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "wait"}}
	for _, tc := range []struct {
		name    string
		history []api.Message
		want    bool
	}{
		{"exhausted, not yet settled", []api.Message{carrier("Status"), refused, carrier("")}, true},
		{"a wait went ahead", []api.Message{carrier("Status"), refused, carrier(""), ran, carrier("")}, false},
		{"newer text is new debt", []api.Message{carrier("Status"), refused, carrier(""), ran, carrier("Changed")}, true},
		{"a wait before refusal settles nothing", []api.Message{carrier("Status"), ran, refused, carrier("")}, true},
		{"no debt", []api.Message{carrier("")}, false},
	} {
		if got := DeriveWaitVisibility(tc.history).NeedsFallback(); got != tc.want {
			t.Errorf("%s: needs fallback = %v", tc.name, got)
		}
	}
}
