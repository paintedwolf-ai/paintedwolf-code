package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/toolreach"
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

func TestFormatOverlayPromoteHostContentRecipientAuthority(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	t.Cleanup(func() { guidance.SetGuidanceRenderer(nil) })

	out := api.WorkerMergeResult{
		JobID:        "job-a",
		ResultHandle: "result-a",
		Status:       api.WorkerMergeStatusMerged,
		Rebased: []api.OverlayRebaseOutcome{{
			OverlayID: "result-b",
			Status:    api.WorkerMergeStatusRebasing,
			Conflicts: []string{"pkg/main.go"},
		}},
	}

	withoutReach, err := formatOverlayPromoteHostContent(context.Background(), "result-a", out, nil)
	if err != nil {
		t.Fatalf("formatOverlayPromoteHostContent without reach: %v", err)
	}
	if !strings.Contains(withoutReach, "Code: OVERLAY_REBASE_CONFLICT") || !strings.Contains(withoutReach, "`pkg/main.go`") {
		t.Fatalf("missing conflict occurrence details:\n%s", withoutReach)
	}
	if strings.Contains(withoutReach, "`preview_overlay`") || strings.Contains(withoutReach, "`promote_overlay`") || strings.Contains(withoutReach, "`instruct_worker`") {
		t.Fatalf("unestablished context leaked stock tool instructions:\n%s", withoutReach)
	}

	admittedCtx := toolreach.WithRequest(t.Context(), promptstest.StockToolReach(t, "preview_overlay"))
	withPreview, err := formatOverlayPromoteHostContent(admittedCtx, "result-a", out, nil)
	if err != nil {
		t.Fatalf("formatOverlayPromoteHostContent with preview reach: %v", err)
	}
	if !strings.Contains(withPreview, "`preview_overlay`") || strings.Contains(withPreview, "`promote_overlay`") {
		t.Fatalf("admitted reach did not match rendered instructions:\n%s", withPreview)
	}
}
