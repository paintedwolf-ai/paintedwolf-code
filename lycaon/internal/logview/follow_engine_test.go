package logview

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFollowEngineSkipsBacklogThenStreams(t *testing.T) {
	eng := NewFollowEngine()
	rec1 := LLMRecord{SessionID: "w1", AgentType: "command-verifier", Messages: []LLMMessage{
		{ID: "u1", Role: "user", Content: rawStr(t, "start")},
		{ID: "a1", Role: "assistant", Content: rawStr(t, "r"), ToolCalls: []LLMToolCall{{Name: "command"}}},
		{ID: "t1", Role: "tool", Content: rawStr(t, "old output")},
	}}
	if evs := eng.LLMEvents([]LLMRecord{rec1}); len(evs) != 0 {
		t.Fatalf("first sight should skip the backlog, got %d events", len(evs))
	}

	rec2 := rec1
	rec2.Messages = append(append([]LLMMessage{}, rec1.Messages...),
		LLMMessage{ID: "a2", Role: "assistant", Content: rawStr(t, "r2"), ToolCalls: []LLMToolCall{
			{Name: "read", Args: json.RawMessage(`{"path":"a.py"}`)},
		}},
		LLMMessage{ID: "t2", Role: "tool", Content: rawStr(t, `[read#1] {"content":"1\tx\n2\ty\n3\tz"}`)},
	)
	evs := eng.LLMEvents([]LLMRecord{rec2})
	// The assistant prose ("r2") becomes a say event and the call+result pair into
	// one tool event carrying the outcome.
	var say, tool *Event
	for i := range evs {
		switch evs[i].Kind {
		case EventSay:
			say = &evs[i]
		case EventTool:
			tool = &evs[i]
		case EventSession, EventSpawn, EventUser, EventDone, EventReject, EventStall:
		}
	}
	if say == nil || say.Summary != "r2" {
		t.Errorf("expected a say event with the prose, got %+v", say)
	}
	if tool == nil || tool.Tool != "read" {
		t.Fatalf("expected a paired read tool event, got %+v", tool)
	}
	if !strings.Contains(tool.Summary, "a.py") || !strings.Contains(tool.Summary, "3 lines") {
		t.Errorf("paired summary should describe the read: %q", tool.Summary)
	}
}

func TestToolSummary(t *testing.T) {
	cases := []struct {
		name, args, result, want string
		wantErr                  bool
	}{
		{"read", `{"path":"scraper.py"}`, `[read#1] {"content":"1\ta\n2\tb\n3\tc"}`, "scraper.py · 3 lines", false},
		{"command", `{"command":"pytest"}`, `[command#1] {"ExitCode":0}`, "exit 0", false},
		{"command", `{"command":"make build"}`, `[command#2] {"ExitCode":2}`, "exit 2", true},
		{"grep", `{"pattern":"TODO"}`, `[grep#1] {"matches":[1,2,3]}`, "3 matches", false},
		{"list_dir", `{"path":"tests"}`, `[list_dir#1] {"entries":[1,2,3,4]}`, "4 entries", false},
		{"update_progress", `{"content":"## Progress\n- [x] a\n- [ ] b\n- [ ] c"}`, ``, "1/3 steps", false},
		{"scan_query", `{}`, `[scan#1] {"findings":[1,2,3,4,5,6,7]}`, "7 findings", false},
	}
	for _, c := range cases {
		s, failed := toolSummary(c.name, json.RawMessage(c.args), c.result)
		if !strings.Contains(s, c.want) {
			t.Errorf("%s: summary %q missing %q", c.name, s, c.want)
		}
		if failed != c.wantErr {
			t.Errorf("%s: failed=%v want %v", c.name, failed, c.wantErr)
		}
	}
}

func TestFollowEngineDoneAndReject(t *testing.T) {
	eng := NewFollowEngine()
	base := LLMRecord{SessionID: "c", AgentType: "coordinator", Messages: []LLMMessage{
		{Role: "user", Content: rawStr(t, "x")},
	}}
	eng.LLMEvents([]LLMRecord{base}) // baseline

	rec := LLMRecord{SessionID: "c", AgentType: "coordinator", Messages: []LLMMessage{
		{Role: "user", Content: rawStr(t, "x")},
		{Role: "tool", Content: rawStr(t, `<task child_session_id="w1" agent_type="command-verifier" state="complete"><report_json>{"leg_status":"complete"}</report_json></task>`)},
		{Role: "user", Content: rawStr(t, "Rejected: nope\nCode: COMMAND_NOT_ARGV")},
	}}
	evs := eng.LLMEvents([]LLMRecord{rec})
	kinds := map[EventKind]Event{}
	for _, e := range evs {
		kinds[e.Kind] = e
	}
	if d, ok := kinds[EventDone]; !ok || d.Agent != "command-verifier" || d.SessionID != "w1" {
		t.Errorf("done event = %+v", kinds[EventDone])
	}
	if r, ok := kinds[EventReject]; !ok || !strings.Contains(r.Summary, "COMMAND_NOT_ARGV") {
		t.Errorf("reject event = %+v", kinds[EventReject])
	}
}

func TestFollowEngineEmitsSayAndUser(t *testing.T) {
	eng := NewFollowEngine()
	eng.LLMEvents([]LLMRecord{{SessionID: "c", AgentType: "coordinator", Iteration: 1, Messages: []LLMMessage{
		{ID: "m0", Role: "user", Content: rawStr(t, "go")},
	}}}) // baseline

	rec := LLMRecord{SessionID: "c", AgentType: "coordinator", Iteration: 2, Messages: []LLMMessage{
		{ID: "m0", Role: "user", Content: rawStr(t, "go")},
		{ID: "m1", Role: "assistant", Content: rawStr(t, "I will read the scraper to understand the flow")},
		{ID: "m2", Role: "user", Content: rawStr(t, ">>> Close finished steps before moving on\nCode: PROGRESS_ITEM_NOT_CLOSED")},
		{ID: "m3", Role: "user", Content: rawStr(t, "[host:loop-wake]")},
	}}
	kinds := map[EventKind]Event{}
	for _, e := range eng.LLMEvents([]LLMRecord{rec}) {
		kinds[e.Kind] = e
	}
	if s, ok := kinds[EventSay]; !ok || !strings.Contains(s.Summary, "read the scraper") || s.Turn != 2 {
		t.Errorf("expected a say event tagged turn 2, got %+v", kinds[EventSay])
	}
	if u, ok := kinds[EventUser]; !ok || !strings.Contains(u.Summary, "Close finished steps") {
		t.Errorf("expected a guidance user event, got %+v", kinds[EventUser])
	}
	if _, isReject := kinds[EventReject]; isReject {
		t.Error("a host loop-wake must not produce an event")
	}
}

func TestBuildEventLogEmitsFullHistory(t *testing.T) {
	sessions := []SessionRecord{
		{SessionID: "c", AgentType: "coordinator", Task: "Do the thing", TS: time.Unix(100, 0)},
	}
	llm := []LLMRecord{
		{SessionID: "c", AgentType: "coordinator", Iteration: 1, Messages: []LLMMessage{
			{ID: "a1", Role: "assistant", Content: rawStr(t, "thinking out loud"), TS: time.Unix(101, 0)},
			{ID: "t1", Role: "tool", Content: rawStr(t, "[read#1] {\"content\":\"1\\n2\"}")},
		}},
	}
	denPerf := []DenPerfRecord{{
		TS:     time.Unix(101, 500_000_000),
		Event:  "loop-stall",
		Detail: map[string]any{"lag_ms": float64(748), "recent": "thumbnail.capture:start"},
	}}
	// Static logs include backlog events.
	log := BuildEventLog(sessions, llm, denPerf)
	kinds := map[EventKind]int{}
	for _, e := range log {
		kinds[e.Kind]++
	}
	if kinds[EventSession] == 0 || kinds[EventSay] == 0 || kinds[EventTool] == 0 || kinds[EventStall] == 0 {
		t.Errorf("event log should include session, prose, tool, and stall: %+v", kinds)
	}
	// Sorted chronologically; stall sits between prose (101s) and later events.
	for i := 1; i < len(log); i++ {
		if log[i].Time.Before(log[i-1].Time) {
			t.Errorf("event log must be chronological")
		}
	}
	stallIdx := -1
	for i, e := range log {
		if e.Kind == EventStall {
			stallIdx = i
			if !strings.Contains(e.Summary, "748ms") {
				t.Errorf("stall summary = %q", e.Summary)
			}
		}
	}
	if stallIdx <= 0 {
		t.Fatalf("stall should be interleaved after session start, index=%d", stallIdx)
	}
}

func TestToolAndTurnLocators(t *testing.T) {
	a := &Agent{Turns: []LLMRecord{
		{Iteration: 5, Messages: []LLMMessage{
			{Role: "assistant", ToolCalls: []LLMToolCall{{Name: "read", ID: "r1"}, {Name: "command", ID: "b1"}}},
			{Role: "tool", Content: rawStr(t, "x")},
			{Role: "tool", Content: rawStr(t, "y")},
		}},
	}}
	tools := a.ToolEvents()
	if ToolIndexByCallID(tools, "b1") != 1 {
		t.Errorf("command call should be at index 1")
	}
	if ToolIndexByCallID(tools, "nope") != -1 {
		t.Errorf("unknown call id should be -1")
	}
	if a.TurnIndexByIteration(5) != 0 || a.TurnIndexByIteration(9) != -1 {
		t.Errorf("turn lookup by iteration wrong")
	}
}

func TestFollowEngineSessionEvents(t *testing.T) {
	eng := NewFollowEngine()
	evs := eng.SessionEvents([]SessionRecord{
		{SessionID: "c", AgentType: "coordinator", Task: "Do the thing"},
		{SessionID: "w", ParentSessionID: "c", AgentType: "command-verifier", Task: "\n- mode: read\n- paths:\n- tests"},
	})
	if len(evs) != 2 || evs[0].Kind != EventSession || evs[1].Kind != EventSpawn {
		t.Fatalf("expected session+spawn, got %+v", evs)
	}
	if !strings.Contains(evs[0].Summary, "Do the thing") {
		t.Errorf("session summary = %q", evs[0].Summary)
	}
	if again := eng.SessionEvents([]SessionRecord{{SessionID: "c", AgentType: "coordinator"}}); len(again) != 0 {
		t.Errorf("already-seen sessions should not re-emit, got %d", len(again))
	}
}

func TestEventLineParsableColumns(t *testing.T) {
	d := NewDisplay(DefaultConfig(), false)
	line := d.EventLine(Event{Kind: EventTool, Agent: "command-verifier", Summary: "→ command"})
	fields := strings.Fields(line)
	// time, kind, agent, then summary
	if len(fields) < 4 || fields[1] != "tool" {
		t.Errorf("event line not column-parsable: %q", line)
	}
}
