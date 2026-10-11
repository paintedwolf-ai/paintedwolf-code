package toolusage

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	sessionobservation "github.com/lycaon/lycaon/internal/session/observation"
	"github.com/lycaon/lycaon/internal/session/store"
)

func TestApplicationObservationPreservesFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		session, code, kind string
		retry               bool
	}{
		{"child", "provider_overloaded", "provider", true},
		{"root", "", "application", false},
		{"child", "provider_request_rejected", "provider", false},
		{"child", "provider_empty_completion", "provider", true},
	} {
		t.Run(tc.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(sessionobservation.ExecutionObservation{SessionID: "root", SubmissionID: "admitted", Failures: []store.ExecutionSubmission{{SessionID: tc.session, ErrorCode: tc.code}}})
			}))
			defer server.Close()
			client := liveClient{base: server.URL, http: server.Client()}
			_, err := client.observeSubmission(t.Context(), "root", "admitted")
			var failure *ExecutionFailure
			if !errors.As(err, &failure) || failure.Kind != tc.kind || failure.Retryable != tc.retry || failure.Code == "" {
				t.Fatalf("failure=%v", err)
			}
		})
	}
}

func TestMalformedModelCallIsNotProviderInfrastructure(t *testing.T) {
	failure := submissionFailure("provider_tool_calls_in_prose")
	if failure.Kind != "model" || failure.Retryable {
		t.Fatalf("malformed model response classification = %+v", failure)
	}
}

func TestInterruptedProviderResponseDoesNotAuthorizeEpisodeReplay(t *testing.T) {
	failure := submissionFailure("provider_response_interrupted")
	if failure.Kind != "provider" || failure.Retryable {
		t.Fatalf("response interruption must be unmeasured without replay: %+v", failure)
	}
}

func TestExecutionErrorsAlwaysCarryFailureMetadata(t *testing.T) {
	for _, started := range []bool{false, true} {
		result := failedSuiteCase(CaseReport{}, errors.New("diagnostic payload"), started)
		if result.Status != "error" || result.Failure == nil || result.Failure.Kind != "harness" || result.Failure.Code == "" || result.Failure.Retryable {
			t.Fatalf("unclassified failure: %+v", result)
		}
	}
	result := failedSuiteCase(CaseReport{}, &ExecutionFailure{Kind: "model", Code: "task_allowance_exhausted"}, true)
	if result.Status != "failed" {
		t.Fatalf("model failure lost: %+v", result)
	}
}

func TestWorkerTaskFailureRemainsAvailableToCoordinator(t *testing.T) {
	observation := sessionobservation.ExecutionObservation{SessionID: "root", SubmissionID: "admitted", Failures: []store.ExecutionSubmission{{SessionID: "child", ErrorCode: "worker_task_failed"}}}
	if err := executionObservationFailure(observation); err != nil {
		t.Fatalf("worker outcome stopped coordinator: %v", err)
	}
	observation.Failures[0].SessionID = "root"
	if err := executionObservationFailure(observation); err == nil {
		t.Fatal("root execution failure was ignored")
	}
}
