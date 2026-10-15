package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
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
		compactionFixtureMessage(t, "body", "tool", "", &api.ToolResult{Tool: "recall", ToolCallID: "recover", Content: `{"resolution":"matched","history":{"entries":[{"message_id":"old","content":"old bytes"}]}}`}, nil),
	}
	labels := canonicalRecoveryLabels("session", messages, 1, len(messages), candidates)
	if labels.Needed["old"] == nil || !*labels.Needed["old"] || labels.Provenance["old"] != "canonical_recovery" {
		t.Fatal("successful body recovery was not recorded")
	}
	if labels.Needed["unused"] != nil || labels.Later["unused"] != nil {
		t.Fatal("absence became an automatic negative label")
	}
	messages[3] = compactionFixtureMessage(t, "body", "tool", "", &api.ToolResult{Tool: "recall", ToolCallID: "recover", Content: `{"resolution":"no_match_in_scope","history":{"entries":[{"message_id":"old","content":"old bytes"}]}}`}, nil)
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

func TestExportCompactionObservationsAndRequestUsage(t *testing.T) {
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(t.TempDir(), "store.db"))
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO llm_calls (id, session_id, caller, status, prompt_tokens, completion_tokens, cache_read_tokens, started_at, usage_source)
		VALUES
			('c-before', 'sess-1', 'coordinator', 'reported', 100, 10, 0, '2026-10-14T00:00:00Z', 'provider'),
			('c-active', 'sess-1', 'coordinator', 'reported', 1200, 150, 800, '2026-10-14T00:00:02Z', 'provider'),
			('c-host', 'sess-1', 'coordinator', 'reported', 50, 5, 0, '2026-10-14T00:00:03Z', 'host')
	`)
	testutil.FailErr(t, "seed llm_calls", err)

	m0 := compactionFixtureMessage(t, "u0", "user", "initial", nil, nil)
	m0.Origin, m0.Ts = string(api.MessageOriginUser), "2026-10-14T00:00:00Z"
	old := compactionFixtureMessage(t, "old", "tool", "", &api.ToolResult{Tool: "read", Content: "old bytes"}, nil)
	old.Ord, old.Seq, old.Ts = 1, 1, "2026-10-14T00:00:00Z"
	open := compactionFixtureMessage(t, "open", "user", "continue", nil, nil)
	open.Origin, open.Ts = string(api.MessageOriginUser), "2026-10-14T00:00:01Z"
	recallArgs := map[string]any{"history": map[string]any{"through_ord": 1, "message_id": "old"}}
	call := compactionFixtureMessage(t, "call", "assistant", "checking", nil, []api.ToolCall{
		{ID: "rc-1", Name: "recall", Args: recallArgs},
		{ID: "rc-2", Name: "recall", Args: recallArgs},
	})
	call.Ts = "2026-10-14T00:00:02Z"
	next := compactionFixtureMessage(t, "next", "user", "done", nil, nil)
	next.Origin, next.Ts = string(api.MessageOriginUser), "2026-10-14T00:00:05Z"
	msgs := []db.ListSessionMessagesRow{m0, old, open, call, next}

	var retentionBuf, requestsBuf bytes.Buffer
	exporter := rowExporter{
		queries:   db.New(sqlDB),
		retention: json.NewEncoder(&retentionBuf),
		requests:  json.NewEncoder(&requestsBuf),
	}
	receipt := db.ListTurnReceiptContextsRow{
		ID:               7,
		SessionID:        "sess-1",
		OpeningMessageID: sql.NullString{String: "open", Valid: true},
		SurfaceID:        "implement",
		CatalogRevision:  "rev-1",
		ProjectName:      "demo",
		Model:            "gemma4:e4b",
		TurnStatus:       "complete",
		StateJson:        `{"host":"linux","user":"continue"}`,
	}
	testutil.FailErr(t, "export observations and usage", exporter.exportCompactionObservations(t.Context(), receipt, "root-1", msgs))

	var retRow retentionRow
	testutil.FailErr(t, "decode retention row", json.Unmarshal(retentionBuf.Bytes(), &retRow))
	if retRow.Schema != retentionRowSchema || len(retRow.Candidates) != 1 || retRow.Candidates[0].ID != "old" {
		t.Fatalf("unexpected retention row: %+v", retRow)
	}

	var reqRow map[string]any
	testutil.FailErr(t, "decode request usage row", json.Unmarshal(requestsBuf.Bytes(), &reqRow))
	if reqRow["schema"] != "pw-request-row/1" || reqRow["prompt_tokens"] != float64(1200) || reqRow["cache_read_tokens"] != float64(800) || reqRow["recall_calls"] != float64(2) || reqRow["repeated_calls"] != float64(1) || reqRow["unavailable_usage_calls"] != float64(1) {
		t.Fatalf("unexpected request usage row: %+v", reqRow)
	}
}

