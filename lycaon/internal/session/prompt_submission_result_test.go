package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
)

// An interrupted receipt records a stopped turn, not a broken one. The async
// runner reads this error to decide whether to publish a host error, so a park
// that loses the sentinel reaches the user as a prompt failure.
func TestPromptSubmissionResultInterruptedCarriesStopSentinel(t *testing.T) {
	_, err := promptSubmissionResult(&store.PromptSubmission{
		ID:     "sub-1",
		Status: store.PromptSubmissionInterrupted,
		Error:  "session is stopping",
	})
	if err == nil {
		t.Fatal("an interrupted receipt must still report why it produced no result")
	}
	if !errors.Is(err, lifecycle.ErrStopping) {
		t.Fatalf("interrupted receipt error = %v want errors.Is ErrSessionStopping", err)
	}
	if !strings.Contains(err.Error(), "sub-1") {
		t.Fatalf("interrupted receipt error = %q want the submission id for logs", err)
	}
}

// A failed receipt is a real failure and stays reportable.
func TestPromptSubmissionResultFailedIsNotAStop(t *testing.T) {
	_, err := promptSubmissionResult(&store.PromptSubmission{
		ID:     "sub-2",
		Status: store.PromptSubmissionFailed,
		Error:  "dial tcp 127.0.0.1:11434: connection refused",
	})
	if err == nil {
		t.Fatal("a failed receipt must report its failure")
	}
	if errors.Is(err, lifecycle.ErrStopping) {
		t.Fatalf("failed receipt error = %v must not be classified as a stop", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("failed receipt error = %q want the stored cause", err)
	}
}
