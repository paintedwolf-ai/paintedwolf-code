package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/ingestion"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
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
