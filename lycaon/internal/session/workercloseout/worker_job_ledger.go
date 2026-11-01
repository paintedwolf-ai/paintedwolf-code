package workercloseout

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

type workerJobLedgerReader struct {
	source      guidance.EvidenceLedgerReader
	handles     map[string]struct{}
	responses   map[string]struct{}
	transcripts workerJobMessagesReader
	workerJobID string
}

type workerJobMessagesReader interface {
	GetWorkerJobMessages(ctx context.Context, sessionID, workerJobID string) ([]api.Message, error)
}

func LedgerForMessages(source guidance.EvidenceLedgerReader, messages []api.Message) guidance.EvidenceLedgerReader {
	if source == nil {
		return nil
	}
	return workerJobLedgerReader{source: source, handles: evidenceHandlesForMessages(messages), responses: sourceResponseIDsForMessages(messages)}
}

func ledgerReaderForWorkerJob(
	source guidance.EvidenceLedgerReader,
	transcripts workerJobMessagesReader,
	workerJobID string,
) guidance.EvidenceLedgerReader {
	if source == nil || transcripts == nil || strings.TrimSpace(workerJobID) == "" {
		return nil
	}
	return workerJobLedgerReader{
		source:      source,
		transcripts: transcripts,
		workerJobID: strings.TrimSpace(workerJobID),
	}
}

func (reader workerJobLedgerReader) LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error) {
	ledger, err := reader.source.LoadLedger(ctx, sessionID)
	if err != nil {
		return evidence.Ledger{}, err
	}
	handles, responses := reader.handles, reader.responses
	if reader.transcripts != nil {
		messages, err := reader.transcripts.GetWorkerJobMessages(ctx, sessionID, reader.workerJobID)
		if err != nil {
			return evidence.Ledger{}, err
		}
		handles = evidenceHandlesForMessages(messages)
		responses = sourceResponseIDsForMessages(messages)
	}
	return filterEvidenceLedger(ledger, handles, responses), nil
}

func evidenceHandlesForMessages(messages []api.Message) map[string]struct{} {
	handles := make(map[string]struct{})
	for _, message := range messages {
		for _, handle := range message.EvidenceHandles {
			handle = strings.TrimSpace(handle)
			if handle != "" {
				handles[handle] = struct{}{}
			}
		}
	}
	return handles
}

// Source deliveries retain their actual command response identity even when
// their handle was committed separately from the transcript message.
func sourceResponseIDsForMessages(messages []api.Message) map[string]struct{} {
	responses := make(map[string]struct{})
	for _, message := range messages {
		result := message.ToolResult
		if message.Role != api.MessageRoleTool || result == nil || (result.Tool != "command" && result.Tool != "command_output") {
			continue
		}
		receipt := result.Invocation
		if receipt != nil && receipt.Invoked && receipt.ID != "" && result.ToolCallID != "" && receipt.Tool == result.Tool && receipt.ToolCallID == result.ToolCallID {
			responses[receipt.ID] = struct{}{}
		}
	}
	return responses
}

func filterEvidenceLedger(ledger evidence.Ledger, handles, responses map[string]struct{}) evidence.Ledger {
	records := make([]evidence.Record, 0, len(handles))
	for handle, record := range ledger.Handles {
		_, included := handles[handle]
		if !included && record.SourceTool == "command" && record.Kind == "read" && record.SourceDelivery != nil {
			_, included = responses[record.SourceDelivery.ResponseID]
		}
		if included {
			records = append(records, record)
		}
	}

	return evidence.AssembleLedger(records)
}
