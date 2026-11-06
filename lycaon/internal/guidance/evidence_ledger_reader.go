package guidance

import (
	"context"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

// EvidenceLedgerReader loads the durable evidence ledger for a session.
type EvidenceLedgerReader interface {
	LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error)
}

// EvidenceLedgerWriter persists evidence at tool-commit time.
type EvidenceLedgerWriter interface {
	CommitProgramObservation(ctx context.Context, sessionID, tool string, receipt *api.CommandProgramReceipt, content string) (string, error)
	CommitEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content string) (handle string, patchedContent string, err error)
	// CommitVisualEvidenceToolResult also stamps the minted handle onto the
	// result's stored visual artifact in the same commit.
	CommitVisualEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content, artifactID string) (handle string, patchedContent string, err error)
}

// EvidenceLedgerStore combines read and write paths for the session evidence ledger.
type EvidenceLedgerStore interface {
	EvidenceLedgerReader
	EvidenceLedgerWriter
}
