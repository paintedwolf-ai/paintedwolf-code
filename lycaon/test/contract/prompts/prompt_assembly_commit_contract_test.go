package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// commitPathForbiddenTokens — summarize_pack trim and TrimSummarizeChunk must
// not appear on the tool-wire commit path (prepareToolWireContent / CompactToolWireIfOversized).
// Assembly invokes TrimSummarizeChunk from the pipeline instead.
var commitPathForbiddenTokens = []string{
	"summarize_pack",
	"TrimSummarizeChunk",
}

var commitPathScanFiles = []string{
	"lycaon/internal/session/tool_wire.go",
	"lycaon/internal/llm/compaction/tool_wire_compact.go",
}

func TestPromptAssemblyCommitPathNoSummarizePackTrim(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range commitPathScanFiles {
		data, err := os.ReadFile(filepath.Join(root, rel))
		contractcheck.FailErr(t, "read "+rel, err)
		text := string(data)
		for _, tok := range commitPathForbiddenTokens {
			if strings.Contains(text, tok) {
				t.Fatalf("%s must not reference %q — summarize trim is assembly-only", rel, tok)
			}
		}
	}
}
