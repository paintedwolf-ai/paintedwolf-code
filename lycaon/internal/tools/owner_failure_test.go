package tools

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOwnerFailurePreservesTimeoutFacts(t *testing.T) {
	for _, command := range []bool{false, true} {
		deadline := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
		err := fmt.Errorf("git status: %w", &exec.TimeoutError{
			Elapsed: 9 * time.Second, Deadline: deadline, Cause: context.DeadlineExceeded, CommandDeadline: command,
		})
		reject := toolrejection.OwnerFailure("git_status", "git", err)
		if reject.Code != toolrejection.ToolOwnerFailedCode || reject.FailureClass != api.FailureClassOwnerError || reject.OwnerRef != "git" {
			t.Fatalf("failure identity = %+v", reject)
		}
		facts := reject.Data["timeout"].(map[string]any)
		if facts["elapsed_ms"] != int64(9000) || facts["deadline"] != "2026-09-17T12:00:00Z" || (facts["source"] == "command") != command {
			t.Fatalf("timeout facts = %+v", facts)
		}
	}
	if reject := toolrejection.OwnerFailure("git_status", "git", errors.New("other failure")); reject.Data["timeout"] != nil {
		t.Fatalf("invented timeout facts: %+v", reject)
	}
}
