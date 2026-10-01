package api_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	wire "github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

// TestSessionRoundTripProperty asserts that any valid Session value survives a
// JSON marshal+unmarshal cycle byte-identical. Catches drift where a new field
// is added on one side of the wire without round-tripping (omitempty mistakes,
// custom UnmarshalJSON that drops fields, time zone normalization, etc.).
func TestSessionRoundTripProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := wire.Session{
			ID:                   rapid.StringMatching(`[a-f0-9-]{8,36}`).Draw(t, "id"),
			ProjectID:            rapid.StringMatching(`[a-z0-9-]{0,36}`).Draw(t, "project_id"),
			WorkspacePath:        rapid.StringMatching(`/[a-z0-9/_-]{1,40}`).Draw(t, "workspace_path"),
			Posture:              drawSessionPosture(t),
			Status:               drawSessionStatus(t),
			AgentType:            rapid.StringMatching(`[a-z_]{0,20}`).Draw(t, "agent_type"),
			ProviderID:           rapid.StringMatching(`[a-z0-9-]{0,30}`).Draw(t, "provider_id"),
			Model:                rapid.StringMatching(`[a-z0-9._-]{0,40}`).Draw(t, "model"),
			ParentSessionID:      rapid.StringMatching(`[a-f0-9-]{0,36}`).Draw(t, "parent_session_id"),
			MaxToolLoops:         rapid.IntRange(0, 1000).Draw(t, "max_tool_loops"),
			CompactionGeneration: rapid.IntRange(0, 1000).Draw(t, "compaction_generation"),
			CreatedAt:            drawTime(t, "created_at"),
			UpdatedAt:            drawTime(t, "updated_at"),
		}

		body, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got wire.Session
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal: %v\nbody: %s", err, body)
		}
		if !reflect.DeepEqual(s, got) {
			t.Fatalf("roundtrip drift\nwant: %+v\n got: %+v\nbody: %s", s, got, body)
		}
	})
}

// TestMessageRoundTripProperty exercises nested ToolCall arrays and optional
// pointers, which are the parts of the wire most prone to omitempty mistakes.
func TestMessageRoundTripProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		m := wire.Message{
			ID:            rapid.StringMatching(`m-[a-f0-9]{4,16}`).Draw(t, "id"),
			Role:          drawMessageRole(t),
			Content:       rapid.StringMatching(`[a-zA-Z0-9 .,!?-]{0,80}`).Draw(t, "content"),
			Kind:          drawMessageKind(t),
			WorkflowRunID: rapid.StringMatching(`(run-[a-f0-9]{0,12})?`).Draw(t, "workflow_run_id"),
			ToolCalls:     drawToolCalls(t),
			CreatedAt:     drawTime(t, "ts"),
		}
		body, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got wire.Message
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal: %v\nbody: %s", err, body)
		}
		if !reflect.DeepEqual(m, got) {
			t.Fatalf("roundtrip drift\nwant: %+v\n got: %+v\nbody: %s", m, got, body)
		}
	})
}

func drawSessionPosture(t *rapid.T) wire.SessionPosture {
	return rapid.SampledFrom([]wire.SessionPosture{
		wire.SessionPostureSpec,
		wire.SessionPostureBuild,
		wire.SessionPostureOrchestrate,
		wire.SessionPostureVet,
	}).Draw(t, "posture")
}

func drawSessionStatus(t *rapid.T) wire.SessionStatus {
	return rapid.SampledFrom([]wire.SessionStatus{
		wire.SessionStatusPreparing,
		wire.SessionStatusIdle,
		wire.SessionStatusBusy,
		wire.SessionStatusError,
	}).Draw(t, "status")
}

func drawMessageRole(t *rapid.T) wire.MessageRole {
	return rapid.SampledFrom([]wire.MessageRole{
		wire.MessageRoleUser,
		wire.MessageRoleAssistant,
		wire.MessageRoleSystem,
		wire.MessageRoleTool,
	}).Draw(t, "role")
}

func drawMessageKind(t *rapid.T) wire.MessageKind {
	kinds := append([]wire.MessageKind{""}, wire.AllMessageKinds()...)
	return rapid.SampledFrom(kinds).Draw(t, "kind")
}

func drawToolCalls(t *rapid.T) []wire.ToolCall {
	n := rapid.IntRange(0, 3).Draw(t, "tool_call_count")
	if n == 0 {
		return nil
	}
	out := make([]wire.ToolCall, n)
	for i := range out {
		out[i] = wire.ToolCall{
			Name: rapid.StringMatching(`[a-z_][a-z_0-9]{0,20}`).Draw(t, "tool_name"),
			ID:   rapid.StringMatching(`tc-[a-f0-9]{0,12}`).Draw(t, "tool_id"),
		}
	}
	return out
}

// drawTime returns RFC3339-truncated UTC times. The wire transports time as
// RFC3339 strings, which discards sub-second precision and forces UTC; we
// truncate here so the property only asserts what the wire actually preserves.
func drawTime(t *rapid.T, label string) time.Time {
	sec := rapid.Int64Range(1700000000, 2000000000).Draw(t, label)
	return time.Unix(sec, 0).UTC()
}
