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
	return workerJobLedgerReader{source: source, handles: evidenceHandlesForMessages(messages)}
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
	handles := reader.handles
	if reader.transcripts != nil {
		messages, err := reader.transcripts.GetWorkerJobMessages(ctx, sessionID, reader.workerJobID)
		if err != nil {
			return evidence.Ledger{}, err
		}
		handles = evidenceHandlesForMessages(messages)
	}
	return filterEvidenceLedger(ledger, handles), nil
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

func filterEvidenceLedger(ledger evidence.Ledger, handles map[string]struct{}) evidence.Ledger {
	records := make([]evidence.Record, 0, len(handles))
	for handle := range handles {
		if record, ok := ledger.Handles[handle]; ok {
			records = append(records, record)
		}
	}
	return evidence.AssembleLedger(records)
}
