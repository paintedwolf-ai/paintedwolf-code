package search

import (
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

const sourceLedger = "ledger"

// ProjectUntrustedLedgerRecord indexes ledger-only untrusted markers.
// Web and MCP results enter through message projection.
func ProjectUntrustedLedgerRecord(projectID, sessionID string, rec evidence.Record) []IndexRow {
	if !evidence.IsUntrustedLedgerMarker(rec.Handle) {
		return nil
	}
	if !evidence.RecordMarksUntrustedContent(rec) {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	sessionID = strings.TrimSpace(sessionID)
	handle := strings.TrimSpace(rec.Handle)
	if projectID == "" || sessionID == "" || handle == "" {
		return nil
	}
	snippet := strings.TrimSpace(rec.URL)
	if snippet == "" {
		snippet = handle
	}
	return []IndexRow{{
		ID:        RowID(sourceLedger, sessionID, handle),
		ProjectID: projectID,
		Source:    SourceTool,
		HitKind:   HitKindWeb,
		SessionID: sessionID,
		SourceRef: handle,
		Handle:    handle,
		Kind:      strings.TrimSpace(rec.Kind),
		Shape:     strings.TrimSpace(rec.Shape),
		Path:      strings.TrimSpace(rec.Path),
		URL:       strings.TrimSpace(rec.URL),
		Snippet:   snippet,
		Trust:     strings.TrimSpace(rec.Fidelity),
		Untrusted: true,
		TS:        strings.TrimSpace(rec.RecordedAt),
	}}
}
