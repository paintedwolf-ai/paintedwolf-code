package closeoutassembly

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
)

type readySynthesis struct{}

func (readySynthesis) ForSession(context.Context, *api.Session) surface.ImplementSessionState {
	return surface.ImplementSessionState{WrapupGatesLoaded: true, BatchReadyForSynthesis: true}
}

type retainedScaffold struct{}

func (retainedScaffold) CurrentPhase(context.Context, string) string { return "work" }
func (retainedScaffold) ScaffoldVarsForSession(context.Context, string) (map[string]any, error) {
	return map[string]any{"evidence_digest": " Retained grounded summary. "}, nil
}

type forbiddenEmptyCuration struct{ t *testing.T }

func (c forbiddenEmptyCuration) Curate(context.Context, evidence.Ledger, llm.CurationFocus, int) (llm.CurationResult, error) {
	c.t.Error("curator invoked without source messages")
	return llm.CurationResult{}, errors.New("no evidence")
}

func TestSynthesisAssemblyPreservesRetainedDigestWhenTranscriptIsEmpty(t *testing.T) {
	s := New(closeoutSession{}, nil, readySynthesis{}, nil)
	s.SetWorkflow(&WorkflowDomains{Policy: retainedScaffold{}})
	s.SetSynthesisCurator(forbiddenEmptyCuration{t})
	got := s.SynthesisEvidenceForAssembly(t.Context(), &api.Session{ID: "parent"}, "implement_synthesis")
	if got != "Retained grounded summary." {
		t.Fatalf("fallback evidence=%q", got)
	}
	if got = s.SynthesisEvidenceForAssembly(t.Context(), &api.Session{ID: "child", ParentSessionID: "parent"}, "implement_synthesis"); got != "" {
		t.Fatalf("coordinator digest leaked into worker: %q", got)
	}
}

func TestExecutionObservationsUseReceiptsWithinIntent(t *testing.T) {
	command := func(id string) api.Message {
		return api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Tool: "command", Content: "Tests passed", ToolArgs: map[string]any{"command": "python3 -m unittest"},
			Invocation: &api.InvocationReceipt{ID: id, Tool: "command", Invoked: true, SourceVerdict: "passed", SourceRevision: "captured"},
		}}
	}
	messages := []api.Message{command("old"), {Role: api.MessageRoleUser, Content: "new request"},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "update_progress", Outcome: "completed", ToolArgs: map[string]any{"content": "- [x] Run CLI"}}},
		{Role: api.MessageRoleAssistant, Content: "I ran the CLI"},
	}
	for range 8 {
		messages = append(messages, command("current"))
	}
	var out executionObservations
	if err := json.Unmarshal([]byte(executionObservationJSON(messages)), &out); err != nil {
		t.Fatalf("decode observations: %v", err)
	}
	if len(out.Actions) != 6 || out.Omitted != 2 || out.ModelChecklist != "- [x] Run CLI" {
		t.Fatalf("observations: %+v", out)
	}
	for _, action := range out.Actions {
		if action.InvocationID != "current" || action.SourceRevision != "captured" || strings.Contains(action.RequestedCommand, "app.py") {
			t.Fatalf("invented action: %+v", action)
		}
	}
	if got := executionObservationJSON([]api.Message{command("only")}); got != "" {
		t.Fatalf("unsolicited checklist projection: %s", got)
	}
	messages = append(messages, api.Message{Role: api.MessageRoleUser, Content: "another task"})
	if got := executionObservationJSON(messages); got != "" {
		t.Fatalf("prior intent leaked: %s", got)
	}
}
