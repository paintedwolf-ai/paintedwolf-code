package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
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

// One delimiter implementation: a second is a second boundary to learn.
func TestRetrievalMarkerHasOneImplementation(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var offenders []string
	err := filepath.Walk(filepath.Join(root, "lycaon", "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.Contains(path, filepath.Join("lycaon", "internal", "datamark")) {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(src), "⟪") || strings.Contains(string(src), "⟫") {
			offenders = append(offenders, strings.TrimPrefix(path, root+string(filepath.Separator)))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("retrieval delimiter written outside internal/datamark: %v", offenders)
	}
}

// Marking happens where content becomes a message. A handler that marks its own
// output caches a nonce and double-wraps what the seam also marks.
func TestToolHandlersDoNotMarkTheirOwnOutput(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var offenders []string
	for _, pkg := range []string{"webresearch", "mcp", "tools"} {
		dir := filepath.Join(root, "lycaon", "internal", pkg)
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			if strings.Contains(string(src), "datamark.Frame(") {
				offenders = append(offenders, strings.TrimPrefix(path, root+string(filepath.Separator)))
			}
			return nil
		})
	}
	if len(offenders) > 0 {
		t.Fatalf("tool handlers mark their own output: %v — marking belongs to the message seam", offenders)
	}
}
