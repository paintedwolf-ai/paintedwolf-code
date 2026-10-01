package worker

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerHonorsProviderEmptyCompletionRetryability(t *testing.T) {
	for _, retryable := range []bool{false, true} {
		err := fmt.Errorf("worker turn: %w", &failure.ProviderEmptyCompletionError{Reason: "length", Terminal: true, Retryable: retryable})
		if got := workerAttemptRetryable(&api.WorkerTask{Attempt: 1}, err); got != retryable {
			t.Fatalf("retryable=%v: worker retry = %v", retryable, got)
		}
	}
	if workerAttemptRetryable(&api.WorkerTask{Attempt: 1}, failure.ErrProviderOutputTruncated) {
		t.Fatal("worker retried native output truncation")
	}
	if workerAttemptRetryable(&api.WorkerTask{Attempt: 1}, &providerretry.ProviderRequestRejectedError{Status: 400}) {
		t.Fatal("worker retried request rejection")
	}
}
