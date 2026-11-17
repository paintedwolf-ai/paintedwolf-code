package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/pkg/api"
)

// Marking is derived from one axis: did somebody outside this machine write
// these bytes? Every layer must read that same axis rather than keeping a list.
func TestExternalAuthorshipIsOneAxis(t *testing.T) {
	t.Parallel()
	external := []api.MessageOrigin{
		api.MessageOriginRetrieval, api.MessageOriginPeerAgent, api.MessageOriginAttachment,
	}
	internal := []api.MessageOrigin{
		api.MessageOriginHost, api.MessageOriginUser, api.MessageOriginModel,
		api.MessageOriginProject, api.MessageOriginTool,
	}
	for _, o := range external {
		if !api.ExternallyAuthored(o) {
			t.Errorf("%s must count as externally authored", o)
		}
	}
	for _, o := range internal {
		if api.ExternallyAuthored(o) {
			t.Errorf("%s must not count as externally authored", o)
		}
	}
	// A local tool result is untrusted for instruction purposes, but nobody
	// outside wrote it, so it carries no marker.
	if api.ExternallyAuthored(api.MessageOriginTool) {
		t.Fatal("local tool output would be marked, putting a marker on every read")
	}
}

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

// Trust may never increase under transformation: a summary of untrusted content
// must not come out trusted.
func TestTrustNeverIncreasesUnderTransformation(t *testing.T) {
	t.Parallel()
	if got := api.FloorTrustTier(api.ContentTrustTierTrusted, api.ContentTrustTierUntrusted); got != api.ContentTrustTierUntrusted {
		t.Errorf("floor of trusted+untrusted = %q, want untrusted", got)
	}
	if got := api.FloorTrustTier(api.ContentTrustTierTrusted, api.ContentTrustTierUnknown); got != api.ContentTrustTierUnknown {
		t.Errorf("floor of trusted+unknown = %q, want unknown", got)
	}
	if got := api.FloorTrustTier(api.ContentTrustTierUnknown, api.ContentTrustTierUntrusted); got != api.ContentTrustTierUntrusted {
		t.Errorf("floor of unknown+untrusted = %q, want untrusted", got)
	}
	// Nothing consumed is unknown, never trusted.
	if got := api.FloorTrustTier(); got != api.ContentTrustTierUnknown {
		t.Errorf("floor of nothing = %q, want unknown", got)
	}
}
