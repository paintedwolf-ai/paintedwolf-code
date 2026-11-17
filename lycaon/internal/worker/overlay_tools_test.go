package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatOverlayPromoteHostContentPrefix(t *testing.T) {
	out := api.WorkerMergeResult{
		JobID:        "job-a",
		ResultHandle: "result-a",
		Mode:         "merge",
		Status:       api.WorkerMergeStatusMerged,
	}
	content, err := formatOverlayPromoteHostContent(context.Background(), "job-a", out, nil)
	if err != nil {
		t.Fatalf("formatOverlayPromoteHostContent: %v", err)
	}
	if !strings.HasPrefix(content, hostmarker.OverlayPromoteEventPrefix) {
		t.Fatalf("content = %q want host promote prefix", content)
	}
	if !strings.Contains(content, `"result_handle":"result-a"`) || strings.Contains(content, "job-a") {
		t.Fatalf("content = %q want the result handle and no job", content)
	}
}

func TestFormatOverlayPromoteHostContentRejectPayload(t *testing.T) {
	out := api.WorkerMergeResult{
		JobID:        "job-conflict",
		ResultHandle: "result-conflict",
		Conflicts:    []api.WorkerMergeConflict{{Path: "a.go"}},
	}
	content, err := formatOverlayPromoteHostContent(context.Background(), "job-conflict", out,
		errors.New("Rejected: overlay\nCode: OVERLAY_PROMOTE_CONFLICT"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(content, hostmarker.OverlayPromoteEventPrefix) {
		t.Fatalf("content = %q want host promote prefix on reject payload", content)
	}
	if !strings.Contains(content, `"result_handle":"result-conflict"`) || strings.Contains(content, `"job_id"`) {
		t.Fatalf("content = %q want merge JSON payload by handle", content)
	}
}

func TestFormatOverlayPromoteHostContentEmptyPayloadOnBareReject(t *testing.T) {
	content, err := formatOverlayPromoteHostContent(context.Background(), "", api.WorkerMergeResult{},
		errors.New("Rejected: missing\nCode: OVERLAY_PROMOTE_NOT_FOUND"))
	if err == nil {
		t.Fatal("expected error")
	}
	if content != "" {
		t.Fatalf("content = %q want empty", content)
	}
}
