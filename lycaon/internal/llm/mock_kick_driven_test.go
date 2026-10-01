package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestKickDrivenMockDispatchesByKickID(t *testing.T) {
	called := map[string]bool{}
	mock := NewKickDrivenMock(KickDrivenConfig{
		OnPhaseAdvanced: func(_ context.Context, _ Snapshot) modelcall.Completion {
			called["phase-advanced"] = true
			return modelcall.Completion{Content: "ack-phase-advanced"}
		},
		OnLegFinished: func(_ context.Context, _ Snapshot) modelcall.Completion {
			called["leg-finished"] = true
			return modelcall.Completion{Content: "ack-leg"}
		},
		OnWorkerTaskFinished: func(_ context.Context, _ Snapshot) modelcall.Completion {
			called["worker-task-finished"] = true
			return modelcall.Completion{Content: "ack-worker"}
		},
		Fallback: func(_ context.Context, _ Snapshot) modelcall.Completion {
			called["fallback"] = true
			return modelcall.Completion{Content: "fallback"}
		},
	})

	cases := []struct {
		userMsg string
		want    string
	}{
		{"Phase advanced — review updated run context", "phase-advanced"},
		{"Leg finished — review the latest tagged worker summary", "leg-finished"},
		{"Worker task finished — review the tagged worker summary", "worker-task-finished"},
		{"random unrelated text", "fallback"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			out, err := mock.Complete(context.Background(), modelcall.CompletionRequest{
				Messages: []api.Message{{Role: api.MessageRoleUser, Content: c.userMsg}},
			})
			testutil.FailErr(t, "Complete", err)
			if !called[c.want] {
				t.Fatalf("handler %q not called for %q (out=%+v)", c.want, c.userMsg, out)
			}
			called[c.want] = false // reset for next iteration
		})
	}
}

func TestKickDrivenMockPicksUpWorkerSummary(t *testing.T) {
	// The handler should see the latest worker_summary content via the snapshot
	// so tests can model "coordinator looked at the summary content and decided
	// to call task() again".
	var sawSummary string
	mock := NewKickDrivenMock(KickDrivenConfig{
		OnWorkerTaskFinished: func(_ context.Context, snap Snapshot) modelcall.Completion {
			sawSummary = snap.LastWorkerSummary
			return modelcall.Completion{Content: "ok"}
		},
	})

	req := modelcall.CompletionRequest{Messages: []api.Message{
		{Role: api.MessageRoleAssistant, Content: "<worker-summary>did the thing</worker-summary>",
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "j1", Status: "complete"}},
		{Role: api.MessageRoleUser, Content: "Worker task finished — review the tagged worker summary"},
	}}
	_, err := mock.Complete(context.Background(), req)
	testutil.FailErr(t, "Complete", err)

	if !strings.Contains(sawSummary, "did the thing") {
		t.Fatalf("OnWorkerTaskFinished did not see the worker summary; got %q", sawSummary)
	}
}

func TestKickDrivenMockStreamingReturnsContentAndToolCalls(t *testing.T) {
	mock := NewKickDrivenMock(KickDrivenConfig{
		OnPhaseAdvanced: func(_ context.Context, _ Snapshot) modelcall.Completion {
			return modelcall.Completion{
				ToolCalls: []api.ToolCall{{ID: "t1", Name: "task", Args: map[string]any{"agent_type": "implementer"}}},
			}
		},
	})
	ch, err := mock.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Phase advanced — review"}},
	})
	testutil.FailErr(t, "Stream", err)
	var got modelcall.StreamChunk
	for chunk := range ch {
		got = chunk
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "task" {
		t.Fatalf("stream final chunk = %+v want task tool call", got)
	}
}

func TestKickDrivenSnapshotKeepsNewestTypedOutcomeIdentity(t *testing.T) {
	snapshot := buildSnapshot([]api.Message{
		{Content: "older", WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "old"}},
		{WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "new"}},
		{Role: api.MessageRoleUser, Content: "[host:loop-wake]"},
	})
	if snapshot.LastWorkerJobID != "new" || snapshot.LastWorkerSummary != "" {
		t.Fatalf("newest typed outcome lost its identity: %+v", snapshot)
	}
	plain := buildSnapshot([]api.Message{{Role: api.MessageRoleAssistant, Content: "Worker task finished: new"}})
	if plain.LastWorkerJobID != "" {
		t.Fatalf("prose invented worker identity: %+v", plain)
	}
}
