package evidence_test

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

// gitToolMessages builds a tool call and result pair.
func gitToolMessages(tool, content string) []api.Message {
	return []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{Name: tool, ID: "g1", Args: map[string]any{}},
			},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    content,
			ToolResult: &api.ToolResult{Content: content, Outcome: api.ToolResultOutcomeCompleted},
		},
	}
}

func TestGitDiffEvidenceResolvesDeletedFileFinding(t *testing.T) {
	deleted := "lycaon/config/packs/painted-wolf/platform/policy/SYNTH_CITATION_IN_PROSE.yaml"
	// git_diff returns JSON: {"available":true,"diff":"<raw unified diff>"} — the diff
	// string carries a/ b/ paths and the deleted-file header.
	rawDiff := "diff --git a/" + deleted + " b/" + deleted + "\n" +
		"deleted file mode 100644\n" +
		"index 1c0ffee..0000000\n" +
		"--- a/" + deleted + "\n" +
		"+++ /dev/null\n" +
		"@@ -1,3 +0,0 @@\n" +
		"-hint_codes:\n"
	payload, err := json.Marshal(map[string]any{"available": true, "diff": rawDiff})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	ev := ledgertest.BuildFromMessages("", gitToolMessages("git_diff", string(payload)))

	// The deleted path is indexed from the embedded diff (git a/ b/ prefixes stripped).
	if _, ok := evidence.HandleForPath(ev, deleted); !ok {
		t.Fatalf("deleted path %q not observed in ledger ByPath", deleted)
	}

	// Deleted paths resolve through their captured diff.
	res := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path:    deleted,
		Line:    1,
		Excerpt: "deleted file mode 100644",
	}, ev, "")
	if res.Verdict != evidence.VerdictMatched {
		t.Fatalf("verdict = %q want matched (handle=%q)", res.Verdict, res.Handle)
	}

	// A fabricated excerpt for the same observed path stays traced, not matched.
	traced := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path:    deleted,
		Excerpt: "this text is not in the diff",
	}, ev, "")
	if traced.Verdict != evidence.VerdictTraced {
		t.Fatalf("verdict = %q want traced for unmatched excerpt on observed path", traced.Verdict)
	}
}

func TestGitStatusEvidence_indexesJSONPathFields(t *testing.T) {
	// git_status returns JSON with a files[] array of {path, status} objects.
	changed := "docs/grounding.md"
	payload, err := json.Marshal(map[string]any{
		"available": true,
		"branch":    "main",
		"files": []map[string]any{
			{"path": changed, "status": " M"},
			{"path": "lycaon/internal/guidance/grounding_fingerprint.go", "status": " M"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ev := ledgertest.BuildFromMessages("", gitToolMessages("git_status", string(payload)))

	if _, ok := evidence.HandleForPath(ev, changed); !ok {
		t.Fatalf("status path %q not indexed from JSON path field", changed)
	}
}

func TestGitDiffEvidence_statModeIndexesChangedPaths(t *testing.T) {
	changed := "internal/session/worker_cycle.go"
	payload, err := json.Marshal(map[string]any{
		"available": true,
		"files": []map[string]any{
			{"path": changed, "insertions": 12, "deletions": 3},
			{"path": "docs/grounding.md", "insertions": 1, "deletions": 0},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ev := ledgertest.BuildFromMessages("", gitToolMessages("git_diff", string(payload)))
	if _, ok := evidence.HandleForPath(ev, changed); !ok {
		t.Fatalf("stat-mode path %q not indexed from git_diff files[]", changed)
	}
}

func TestCommandEvidence_capturesBodyForExcerptMatch(t *testing.T) {
	out := "running internal/foo/bar.go\nPASS\nok  internal/foo  0.42s\n"
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{Name: "command", ID: "b1", Args: map[string]any{"command": "go test ./internal/foo/"}},
			},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    out,
			ToolResult: &api.ToolResult{Content: out, Outcome: api.ToolResultOutcomeCompleted},
		},
	}
	ev := ledgertest.BuildFromMessages("", msgs)

	// A path named in command output is observed, and an excerpt verbatim in the output
	// verifies against the captured command body (matched, not merely traced).
	res := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path:    "internal/foo/bar.go",
		Excerpt: "ok  internal/foo  0.42s",
	}, ev, "")
	if res.Verdict != evidence.VerdictMatched {
		t.Fatalf("verdict = %q want matched for excerpt in command body", res.Verdict)
	}
}
