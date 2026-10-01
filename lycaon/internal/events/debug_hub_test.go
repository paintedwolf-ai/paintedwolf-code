package events

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWrapDebugHubPassthroughWhenDisabled(t *testing.T) {
	t.Setenv("LYCAON_SSE_DEBUG", "")
	observability.CloseSSEDebug()

	inner := NewMemoryHub()
	wrapped := WrapDebugHub(inner)
	if wrapped != inner {
		t.Fatal("expected same hub when debug disabled")
	}
}

func TestWrapDebugHubLogsPublish(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "sse.jsonl")
	t.Setenv("LYCAON_SSE_DEBUG", "1")
	t.Setenv("LYCAON_SSE_DEBUG_FILE", logPath)
	observability.CloseSSEDebug()

	inner := NewMemoryHub()
	hub := WrapDebugHub(inner)
	if err := hub.Publish(context.Background(), api.EventTopicCheckpoint, PublishKey{Project: "35576509-eb1b-5e0a-b1f5-e9a6512876f5"}, api.CheckpointEvent{ID: "cp-1"}); err != nil {
		testutil.FailErr(t, "hub.Publish failed", err)
	}

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read file", err)
	if len(data) == 0 {
		t.Fatal("expected sse debug log entry")
	}
}
