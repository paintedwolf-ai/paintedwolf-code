package tools

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHostRefusalPreservesRenderedCode(t *testing.T) {
	t.Parallel()
	err := toolrejection.FormatDecisionReject(toolrejection.ToolOwnerFailedCode, nil, nil)
	got := toolrejection.HostRefusal(err)
	if got == nil {
		t.Fatal("expected host refusal")
	}
	if !strings.Contains(got.Error(), "Code: "+toolrejection.ToolOwnerFailedCode) {
		t.Fatalf("refusal = %q", got.Error())
	}
	if toolrejection.HostRefusal(fmt.Errorf("plain owner boom")) != nil {
		t.Fatal("plain error is not a host refusal")
	}
}

// Rendered rejections retain their structured cause.
func TestFormatDecisionRejectKeepsStructuredCause(t *testing.T) {
	t.Parallel()
	err := toolrejection.FormatDecisionReject(toolrejection.ToolOwnerFailedCode, map[string]any{
		"reason": "worker queue refused the enqueue",
	}, nil)
	reject := toolrejection.AsToolReject(err)
	if reject == nil {
		t.Fatal("rendered reject lost its structured cause")
	}
	if reject.Code != toolrejection.ToolOwnerFailedCode {
		t.Fatalf("code = %q want %q", reject.Code, toolrejection.ToolOwnerFailedCode)
	}
	got, _ := reject.Data["reason"].(string)
	if got != "worker queue refused the enqueue" {
		t.Fatalf("reason = %q want the owner's cause", got)
	}
	if !strings.Contains(err.Error(), "worker queue refused the enqueue") {
		t.Fatalf("agent-visible block dropped the cause: %q", err.Error())
	}
}

func TestCompleteFailureMetadataDefaultsToHostRejection(t *testing.T) {
	t.Parallel()
	reject := toolrejection.CompleteFailureMetadata(&toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID"}, "http_request", "http_request")
	if reject.FailureClass != api.FailureClassHostRejection {
		t.Fatalf("failure class = %q want %q", reject.FailureClass, api.FailureClassHostRejection)
	}
	if got := reject.Data["failure_class"]; got != api.FailureClassHostRejection {
		t.Fatalf("failure_class data = %v want %q", got, api.FailureClassHostRejection)
	}
	if got := reject.Data["tool"]; got != "http_request" {
		t.Fatalf("tool data = %v want http_request", got)
	}
	if reject.OwnerRef != "http_request" {
		t.Fatalf("owner ref = %q want http_request", reject.OwnerRef)
	}
}

func TestCompleteFailureMetadataBindsIsolationDisposition(t *testing.T) {
	for _, outcome := range isolation.Outcomes() {
		t.Run(outcome.Code, func(t *testing.T) {
			retryable := outcome.Retryable()
			reject := toolrejection.CompleteFailureMetadata(&toolrejection.ToolReject{
				Code: outcome.Code,
				Data: map[string]any{"failure_class": "forged", "retryable": !retryable},
			}, "command", "processes")
			if reject.FailureClass != api.FailureClassIsolationRejection || reject.Retryable != retryable {
				t.Fatalf("metadata = class %q retryable %v", reject.FailureClass, reject.Retryable)
			}
			if got := reject.Data["isolation_disposition"]; got != string(outcome.Disposition) {
				t.Fatalf("isolation_disposition = %v, want %q", got, outcome.Disposition)
			}
			if reject.Data["failure_class"] != api.FailureClassIsolationRejection || reject.Data["retryable"] != retryable {
				t.Fatalf("structured metadata was not authoritative: %+v", reject.Data)
			}
		})
	}
}

func TestFormatDecisionReject(t *testing.T) {
	t.Parallel()
	err := toolrejection.FormatDecisionReject("GREP_REGEX_INVALID", map[string]any{"pattern": "[", "detail": "missing ]"}, guidance.NewStaticRejectFormatter(moduleHintConfig(t)))
	if err == nil {
		t.Fatal("expected formatted error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "GREP_REGEX_INVALID") || !strings.Contains(msg, "Code:") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestFormatDecisionRejectChmod(t *testing.T) {
	t.Parallel()
	err := toolrejection.FormatDecisionReject("CHMOD_MODE_DENIED", map[string]any{"path": "x", "mode": "0777"}, guidance.NewStaticRejectFormatter(moduleHintConfig(t)))
	if err == nil {
		t.Fatal("expected formatted error")
	}
	if !strings.Contains(err.Error(), "CHMOD_MODE_DENIED") {
		t.Fatalf("msg = %q", err.Error())
	}
}

func TestFormatDecisionRejectDelete(t *testing.T) {
	t.Parallel()
	err := toolrejection.FormatDecisionReject("DELETE_PATH_DENIED", map[string]any{"path": "x"}, guidance.NewStaticRejectFormatter(moduleHintConfig(t)))
	if err == nil {
		t.Fatal("expected formatted error")
	}
	if !strings.Contains(err.Error(), "DELETE_PATH_DENIED") {
		t.Fatalf("msg = %q", err.Error())
	}
}

func TestFormatDecisionRejectMissingHintStillStructured(t *testing.T) {
	t.Parallel()
	err := toolrejection.FormatDecisionReject("NOT_A_REAL_HINT_CODE", map[string]any{"path": "x"}, guidance.NewStaticRejectFormatter(moduleHintConfig(t)))
	if err == nil {
		t.Fatal("expected formatted error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Rejected:") || !strings.Contains(msg, "Code: NOT_A_REAL_HINT_CODE") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestFormatDecisionRejectSummarizeNoInput(t *testing.T) {
	t.Parallel()
	err := toolrejection.FormatDecisionReject("SUMMARIZE_NO_INPUT", nil, guidance.NewStaticRejectFormatter(moduleHintConfig(t)))
	if err == nil {
		t.Fatal("expected formatted error")
	}
	if !strings.Contains(err.Error(), "SUMMARIZE_NO_INPUT") {
		t.Fatalf("msg = %q", err.Error())
	}
}
