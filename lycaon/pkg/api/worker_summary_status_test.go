package api_test

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerSummaryLegSucceeded(t *testing.T) {
	if !api.WorkerSummaryLegSucceeded(api.WorkerSummaryStatusOpen) {
		t.Fatal("open leg should succeed for workflow gates")
	}
	if api.WorkerSummaryLegSucceeded(api.WorkerSummaryStatusPartial) {
		t.Fatal("partial leg should not succeed")
	}
}

func TestWorkerResultStatusReactable(t *testing.T) {
	if !api.WorkerResultStatusReactable("") || !api.WorkerResultStatusReactable(" partial ") {
		t.Fatal("empty and padded complete-equivalent statuses must wake")
	}
	if api.WorkerResultStatusReactable("running") {
		t.Fatal("queue running is not a terminal result")
	}
	for _, status := range api.AllWorkerSummaryStatuses() {
		got := api.WorkerResultStatusReactable(string(status))
		switch status {
		case api.WorkerSummaryStatusCanceled, api.WorkerSummaryStatusHeld:
			if got {
				t.Fatalf("status %q is coordinator-initiated or in-flight and must not wake", status)
			}
		default:
			if !got {
				t.Fatalf("status %q is a parent-visible terminal and must wake", status)
			}
		}
	}
}
