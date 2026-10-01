package logview

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func rawStr(t *testing.T, s string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(s)
	testutil.FailErr(t, "marshal", err)
	return b
}

func TestBuildSessionTreeTopologyAndOutcome(t *testing.T) {
	sessions := []SessionRecord{
		{SessionID: "coord", AgentType: "coordinator", Surface: "implement_investigate", Task: "Tell me about this repo."},
		{SessionID: "w1", ParentSessionID: "coord", AgentType: "command-verifier", Task: "\nTask mode:\n- mode: read\n- suggested paths:\n- tests\n"},
		{SessionID: "w2", ParentSessionID: "coord", AgentType: "command-verifier", Task: "scope"},
	}
	llm := []LLMRecord{
		{SessionID: "coord", AgentType: "coordinator", Iteration: 1, Messages: []LLMMessage{
			{Role: "user", Content: rawStr(t, "Tell me about this repo.")},
		}},
		{SessionID: "coord", AgentType: "coordinator", Iteration: 2, Messages: []LLMMessage{
			{Role: "tool", Content: rawStr(t, `<task child_session_id="w1" state="complete"><report_json>{"leg_status":"complete"}</report_json></task>`)},
		}},
		{SessionID: "w1", AgentType: "command-verifier", Iteration: 1, Messages: []LLMMessage{
			{Role: "user", Content: rawStr(t, "Worker task started")},
		}},
		// w2 has no coordinator envelope and ends on a forced closeout → partial.
		{SessionID: "w2", AgentType: "command-verifier", Iteration: 1, Messages: []LLMMessage{
			{Role: "user", Content: rawStr(t, "[host:worker-closeout]\nThis is a forced final turn.")},
		}},
	}

	tree := BuildSessionTree(sessions, llm)
	if tree.Root == nil || tree.Root.SessionID != "coord" {
		t.Fatalf("root should be the coordinator, got %+v", tree.Root)
	}
	if got := tree.Headline(); got != "Tell me about this repo." {
		t.Errorf("headline = %q", got)
	}
	if len(tree.Agents) != 3 {
		t.Fatalf("expected 3 agents depth-first, got %d", len(tree.Agents))
	}
	if len(tree.Root.Children) != 2 {
		t.Errorf("coordinator should have 2 workers, got %d", len(tree.Root.Children))
	}

	w1, _ := tree.Find("w1")
	if w1.Outcome.Status != StatusComplete {
		t.Errorf("w1 outcome = %q, want complete", w1.Outcome.Status)
	}
	w2, _ := tree.Find("w2")
	if w2.Outcome.Status != StatusPartial {
		t.Errorf("w2 outcome = %q, want partial (forced closeout)", w2.Outcome.Status)
	}
}

func TestMultipleRootSessions(t *testing.T) {
	sessions := []SessionRecord{
		{SessionID: "c1", AgentType: "coordinator", Task: "First request"},
		{SessionID: "c2", AgentType: "coordinator", Task: "Second request"},
	}
	llm := []LLMRecord{
		{SessionID: "c1", AgentType: "coordinator", Iteration: 1, TS: time.Date(2026, 6, 22, 20, 31, 0, 0, time.UTC)},
		{SessionID: "c2", AgentType: "coordinator", Iteration: 1, TS: time.Date(2026, 6, 22, 20, 35, 0, 0, time.UTC)},
	}
	tree := BuildSessionTree(sessions, llm)
	if got := len(tree.Roots()); got != 2 {
		t.Fatalf("a capture with two coordinators should have 2 roots, got %d", got)
	}
}

func TestScopeDescriptor(t *testing.T) {
	task := "\nTask mode:\n- mode: read\n- suggested paths:\n- tests\n- internal\n\nTool budget: 40"
	if got := scopeDescriptor(task); got != "read: tests, internal" {
		t.Errorf("scopeDescriptor = %q", got)
	}
	if got := scopeDescriptor("no scope here"); got != "" {
		t.Errorf("non-scope brief should yield empty, got %q", got)
	}
}
