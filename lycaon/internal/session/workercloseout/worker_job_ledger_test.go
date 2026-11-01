package workercloseout

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type fixedEvidenceLedger struct {
	ledger evidence.Ledger
}

func (reader fixedEvidenceLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return reader.ledger, nil
}

type mutableWorkerMessages struct {
	messages []api.Message
}

func (reader *mutableWorkerMessages) GetWorkerJobMessages(context.Context, string, string) ([]api.Message, error) {
	return append([]api.Message(nil), reader.messages...), nil
}

func TestLedgerReaderForWorkerMessagesExcludesPriorRunEvidence(t *testing.T) {
	source := fixedEvidenceLedger{ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Path: "old.go"},
		{Handle: "read#2", Kind: "read", Path: "current.go"},
	})}
	reader := LedgerForMessages(source, []api.Message{{EvidenceHandles: []string{"read#2"}}})

	ledger, err := reader.LoadLedger(t.Context(), "child")
	testutil.FailErr(t, "load scoped ledger", err)
	if _, ok := ledger.Handles["read#1"]; ok {
		t.Fatal("prior-run evidence remained in scoped ledger")
	}
	if _, ok := ledger.Handles["read#2"]; !ok {
		t.Fatal("current-run evidence missing from scoped ledger")
	}
}

func TestLedgerReaderForWorkerJobIncludesRetryEvidence(t *testing.T) {
	source := fixedEvidenceLedger{ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Path: "initial.go"},
		{Handle: "read#2", Kind: "read", Path: "retry.go"},
	})}
	transcript := &mutableWorkerMessages{messages: []api.Message{{EvidenceHandles: []string{"read#1"}}}}
	reader := ledgerReaderForWorkerJob(source, transcript, "job-1")

	transcript.messages = append(transcript.messages, api.Message{EvidenceHandles: []string{"read#2"}})
	ledger, err := reader.LoadLedger(t.Context(), "child")
	testutil.FailErr(t, "load refreshed ledger", err)
	if _, ok := ledger.Handles["read#2"]; !ok {
		t.Fatal("retry evidence missing from scoped ledger")
	}
}

func TestWorkerJobLedgerBindsNativeSourceToItsActualCommandResponse(t *testing.T) {
	source := fixedEvidenceLedger{ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", SourceTool: "command", SourceDelivery: &evidence.SourceDelivery{ResponseID: "old-response"}},
		{Handle: "read#2", Kind: "read", SourceTool: "command", SourceDelivery: &evidence.SourceDelivery{ResponseID: "current-response"}},
	})}
	for _, mode := range []string{"current", "assistant", "uninvoked", "wrong-call", "blank-call", "wrong-tool"} {
		t.Run(mode, func(t *testing.T) {
			message := api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "command", ToolCallID: "call-current", Invocation: &api.InvocationReceipt{ID: "current-response", Tool: "command", ToolCallID: "call-current", Invoked: true}}}
			switch mode {
			case "assistant":
				message.Role = api.MessageRoleAssistant
			case "uninvoked":
				message.ToolResult.Invocation.Invoked = false
			case "wrong-call":
				message.ToolResult.Invocation.ToolCallID = "other-call"
			case "blank-call":
				message.ToolResult.ToolCallID = ""
				message.ToolResult.Invocation.ToolCallID = ""
			case "wrong-tool":
				message.ToolResult.Tool = "edit"
			}
			ledger, err := LedgerForMessages(source, []api.Message{message}).LoadLedger(t.Context(), "child")
			testutil.FailErr(t, "load native job evidence", err)
			if _, ok := ledger.Handles["read#1"]; ok {
				t.Fatal("earlier job source crossed its response boundary")
			}
			_, present := ledger.Handles["read#2"]
			if present != (mode == "current") {
				t.Fatalf("native source present=%t mode=%s", present, mode)
			}
		})
	}
}
