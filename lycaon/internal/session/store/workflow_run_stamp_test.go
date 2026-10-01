package store

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestRequireWorkflowRunStamp(t *testing.T) {
	t.Parallel()
	if err := requireWorkflowRunStamp(api.Message{Role: api.MessageRoleUser, Content: "hi"}); err != nil {
		t.Fatalf("plain user row: %v", err)
	}
	if err := requireWorkflowRunStamp(api.Message{
		Kind:           api.MessageKindProgressUpdate,
		WorkflowRunID:  "run-1",
		ProgressUpdate: &api.ProgressUpdateMeta{Seq: 1},
	}); err != nil {
		t.Fatalf("stamped progress_update: %v", err)
	}
	err := requireWorkflowRunStamp(api.Message{
		Kind:           api.MessageKindProgressUpdate,
		ProgressUpdate: &api.ProgressUpdateMeta{Seq: 1},
	})
	if !errors.Is(err, ErrWorkflowRunStampRequired) {
		t.Fatalf("err = %v want ErrWorkflowRunStampRequired", err)
	}
}
