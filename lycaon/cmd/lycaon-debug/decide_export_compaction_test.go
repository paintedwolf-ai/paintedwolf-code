package main

import (
	"database/sql"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRetentionTrainingAndReplayKeepUnknownLabelsMasked(t *testing.T) {
	script := `
import sys
sys.path.insert(0, sys.argv[1])
import rows
import retention_eval as evaluation
row = {"partial": False, "state": {"user": "continue"}, "tokens": 200,
       "candidates": [{"id": "a", "ref": "r0", "option": "recorded read", "tokens": 20},
                      {"id": "b", "ref": "r1", "option": "unobserved read", "tokens": 30}],
       "labels": {"needed": {"a": True}, "later": {}, "calls_after": 2,
                  "provenance": {"a": "canonical_recovery"}, "by": {"a": ["receipt"]}}}
assert rows.retention_targets(row) == {"r0": 1, "r1": None}
row["labels"]["needed"]["b"] = False
assert rows.retention_targets(row)["r1"] is None
row["labels"]["provenance"]["b"] = "judge"
row["labels"]["by"]["b"] = ["judge-verdict"]
assert rows.retention_targets(row) == {"r0": 1, "r1": 0}
class Engine:
    def call(self, request):
        assert request["head"] == "retention"
        assert request["questions"]["retention"]["independent"]
        return {"answers": {"retention": {"probabilities": {"r0": 0.9, "r1": 0.1}}}}
groups, missed = evaluation.replay([row], Engine(), "continue", "needed")
report = evaluation.score(groups, 0.2, 0.6, repeats=10)
assert missed == 0 and report["verified_label_rows"] == 1
assert report["engine"]["released"] == 1 and report["engine"]["needed_released"] == 0
del row["labels"]["provenance"]["b"]
groups, _ = evaluation.replay([row], Engine(), "continue", "needed")
report = evaluation.score(groups, 0.2, 0.6, repeats=10)
assert report["verified_label_rows"] == 0 and report["unknown_candidates"] == 1
assert report["engine"]["released"] == 0
`
	root := testutil.CheckoutRoot(t)
	output, err := exec.CommandContext(t.Context(), "python3", "-c", script, filepath.Join(root, "scripts", "bialy")).CombinedOutput()
	if err != nil {
		t.Fatalf("retention label and replay contract: %v\n%s", err, output)
	}
}

func compactionFixtureMessage(t *testing.T, id, role, content string, result *api.ToolResult, calls []api.ToolCall) db.ListSessionMessagesRow {
	t.Helper()
	row := db.ListSessionMessagesRow{ID: id, Role: role, Content: content}
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		row.ToolResultJson = sql.NullString{String: string(raw), Valid: true}
	}
	if calls != nil {
		raw, err := json.Marshal(calls)
		if err != nil {
			t.Fatalf("marshal calls: %v", err)
		}
		row.ToolCallsJson = sql.NullString{String: string(raw), Valid: true}
	}
	return row
}

func TestCompactionLabelsRequireSuccessfulRecoveryAndKeepAbsenceUnknown(t *testing.T) {
	candidates := []retentionCandidate{{ID: "old", Ref: "r0", Handles: []string{"read#1"}}, {ID: "unused", Ref: "r1"}}
	messages := []db.ListSessionMessagesRow{
		compactionFixtureMessage(t, "old", "tool", "old bytes", &api.ToolResult{Tool: "read"}, nil),
		compactionFixtureMessage(t, "request", "user", "continue", nil, nil),
		compactionFixtureMessage(t, "call", "assistant", "", nil, []api.ToolCall{{ID: "recover", Name: "recall", Args: map[string]any{"history": map[string]any{"through_ord": 1, "message_id": "old"}}}}),
		compactionFixtureMessage(t, "body", "tool", "", &api.ToolResult{Tool: "recall", ToolCallID: "recover", Content: `{"resolution":"matched","history":{"entries":[{"id":"old","content":"old bytes"}]}}`}, nil),
	}
	labels := canonicalRecoveryLabels("session", messages, 1, len(messages), candidates)
	if labels.Needed["old"] == nil || !*labels.Needed["old"] || labels.Provenance["old"] != "canonical_recovery" {
		t.Fatal("successful body recovery was not recorded")
	}
	if labels.Needed["unused"] != nil || labels.Later["unused"] != nil {
		t.Fatal("absence became an automatic negative label")
	}
	messages[3] = compactionFixtureMessage(t, "body", "tool", "", &api.ToolResult{Tool: "recall", ToolCallID: "recover", Content: `{"resolution":"no_match_in_scope","history":{"entries":[{"id":"old","content":"old bytes"}]}}`}, nil)
	if labels = canonicalRecoveryLabels("session", messages, 1, len(messages), candidates); labels.Needed["old"] != nil {
		t.Fatal("failed recovery established need")
	}
	messages[3] = compactionFixtureMessage(t, "body", "tool", "", &api.ToolResult{Tool: "recall", ToolCallID: "recover", Content: `{"resolution":"matched","hits":[{"handle":"read#1","session_id":"session","body":["old bytes"]}]}`}, nil)
	if labels = canonicalRecoveryLabels("session", messages, 1, 3, candidates); labels.Needed["old"] != nil || labels.Later["old"] == nil {
		t.Fatal("later-turn recovery crossed the current request's label boundary")
	}
}

func TestCompactionCandidatesBoundInlineFeaturesWithoutDroppingCanonicalIdentity(t *testing.T) {
	var messages []db.ListSessionMessagesRow
	for i := 0; i < 30; i++ {
		row := compactionFixtureMessage(t, string(rune('a'+i)), "tool", "observation", &api.ToolResult{Tool: "read"}, nil)
		row.Ord = int64(i + 1)
		row.Seq = int64(i + 1)
		messages = append(messages, row)
	}
	candidates, tokens := compactionCandidates(messages)
	if len(candidates) != retentionCandidateLimit || candidates[0].Ord != 15 || candidates[15].Ord != 30 || tokens <= 0 {
		t.Fatal("candidate bound lost source coordinates")
	}
	if openingIndex(messages, "absent") != -1 || openingIndex(messages, messages[3].ID) != 3 {
		t.Fatal("opening identity was not exact")
	}
}
