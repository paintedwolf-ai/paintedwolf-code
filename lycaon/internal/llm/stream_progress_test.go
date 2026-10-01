package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCollectStreamWithProgressMergesToolSnapshots(t *testing.T) {
	ch := make(chan modelcall.StreamChunk, 4)
	go func() {
		ch <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{{Name: "write", Args: map[string]any{"path": "a"}}}, Progress: true}
		ch <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{{Name: "write", Args: map[string]any{"path": "a", "content": "hi"}}}, Progress: true}
		ch <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{{Name: "write", Args: map[string]any{"path": "a", "content": "hello"}}}, Done: true}
		close(ch)
	}()
	var progress int
	completion, _, err := modelcall.CollectStreamWithProgress(ch, func(c *modelcall.Completion) {
		progress++
		if c == nil || len(c.ToolCalls) != 1 {
			t.Fatalf("partial = %+v", c)
		}
	})
	testutil.FailErr(t, "CollectStreamWithProgress failed", err)
	if progress < 2 {
		t.Fatalf("progress callbacks = %d", progress)
	}
	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", completion.ToolCalls)
	}
}
