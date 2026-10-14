package main

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

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
	labels := canonicalRecoveryLabels(messages, 1, len(messages), candidates)
	if labels.Needed["old"] == nil || !*labels.Needed["old"] || labels.Provenance["old"] != "canonical_recovery" {
		t.Fatal("successful body recovery was not recorded")
	}
	if labels.Needed["unused"] != nil || labels.Later["unused"] != nil {
		t.Fatal("absence became an automatic negative label")
	}
	messages[3] = compactionFixtureMessage(t, "body", "tool", "", &api.ToolResult{Tool: "recall", ToolCallID: "recover", Content: `{"resolution":"no_match_in_scope","history":{"entries":[{"id":"old","content":"old bytes"}]}}`}, nil)
	if labels = canonicalRecoveryLabels(messages, 1, len(messages), candidates); labels.Needed["old"] != nil {
		t.Fatal("failed recovery established need")
	}
	messages[3] = compactionFixtureMessage(t, "body", "tool", "", &api.ToolResult{Tool: "recall", ToolCallID: "recover", Content: `{"resolution":"matched","hits":[{"handle":"read#1","body":["old bytes"]}]}`}, nil)
	if labels = canonicalRecoveryLabels(messages, 1, 3, candidates); labels.Needed["old"] != nil || labels.Later["old"] == nil {
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
