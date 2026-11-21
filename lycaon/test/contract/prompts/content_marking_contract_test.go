package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/ingestion"
)

// A retrieval tool's output must be stamped with the retrieval origin, or the
// marker, the taint, and the pre-dial exemption all read the wrong answer.
func TestRetrievalToolsMapToExternalAuthorship(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{
		ingestion.ToolWebSearch, ingestion.ToolFetchURL, ingestion.MCPToolPrefix + "acme_deploy",
	} {
		if !ingestion.IsRetrievalTool(tool) {
			t.Errorf("%s is not recognized as a retrieval tool", tool)
			continue
		}
		if !evidence.RecordMarksUntrustedContent(evidence.Record{SourceTool: tool}) {
			t.Errorf("%s retrieves but does not mark the chat as having ingested", tool)
		}
	}
	for _, tool := range []string{"read", "write", "command", "grep"} {
		if ingestion.IsRetrievalTool(tool) || evidence.RecordMarksUntrustedContent(evidence.Record{SourceTool: tool}) {
			t.Errorf("%s is not a retrieval tool", tool)
		}
	}
}
