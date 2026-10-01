package evidence

import (
	"strings"

	"github.com/lycaon/lycaon/internal/ingestion"
)

// Ledger-only untrusted markers (no corresponding tool-result message).
const (
	InheritUntrustedHandle = "untrusted_inherit#1"
	WorkerURLHandlePrefix  = "worker_url#"
)

// IsUntrustedLedgerMarker checks host-authored trust markers.
func IsUntrustedLedgerMarker(handle string) bool {
	h := terminalHandle(handle)
	return h == InheritUntrustedHandle || strings.HasPrefix(h, WorkerURLHandlePrefix)
}

// RecordMarksUntrustedContent reports whether a ledger record means the session
// has ingested untrusted external content.
func RecordMarksUntrustedContent(rec Record) bool {
	if IsUntrustedLedgerMarker(rec.Handle) {
		return true
	}
	return ingestion.IsRetrievalTool(rec.SourceTool)
}
