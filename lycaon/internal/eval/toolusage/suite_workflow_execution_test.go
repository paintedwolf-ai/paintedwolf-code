package toolusage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowScenarioUsesExplicitStartAndTreeSettlement(t *testing.T) {
	for _, status := range []api.WorkflowRunStatus{api.WorkflowRunStatusComplete, api.WorkflowRunStatusRunning} {
		t.Run(string(status), func(t *testing.T) {
			started := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/sessions/root":
					_ = json.NewEncoder(w).Encode(api.Session{ID: "root", Status: api.SessionStatusIdle})
				case "/v1/sessions/root/workflow-runs":
					var request api.StartWorkflowRunRequest
					testutil.FailErr(t, "decode workflow start", json.NewDecoder(r.Body).Decode(&request))
					if request.Request != "Process R17" || request.WorkflowID != "release" || request.WorkflowVersion != "1.0.0" || request.OperationID == "" {
						t.Errorf("workflow request: %+v", request)
					}
					started = true
					_ = json.NewEncoder(w).Encode(api.WorkflowRun{ID: "flow", SessionID: "root", WorkflowID: "release", WorkflowVersion: "1.0.0"})
				case "/harness/workflow-execution/root/flow":
					_ = json.NewEncoder(w).Encode(session.WorkflowExecutionObservation{
						Run:       api.WorkflowRun{ID: "flow", SessionID: "root", WorkflowID: "release", WorkflowVersion: "1.0.0", Status: status},
						Execution: session.ExecutionObservation{SessionID: "root", Settled: true},
					})
				default:
					t.Errorf("unexpected workflow request: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			client := newLiveClient(server.URL, "")
			client.settlementDirectory = t.TempDir()
			result := CaseReport{SessionID: "root", WorkflowID: "release"}
			spec := SuiteCase{WorkflowID: "release", WorkflowVersion: "1.0.0", CorpusTask: CorpusTask{Prompt: "Process R17"}}
			testutil.FailErr(t, "run explicit workflow", client.runWorkflowScenario(t.Context(), &result, spec, func(CaseReport) error { return nil }))
			if !started || result.WorkflowRunID != "flow" {
				t.Fatalf("workflow admission: %+v", result)
			}
			testutil.FailErr(t, "retain workflow settlement", settleCapturedWorkflow(client.settlementDirectory, &result))
			if result.Status != "review_required" {
				t.Fatalf("settled workflow must reach grading: %+v", result)
			}
		})
	}
}

func TestWorkflowObservationRejectsForeignRunAndContradictorySettlement(t *testing.T) {
	result := CaseReport{SessionID: "root", WorkflowID: "release", WorkflowRunID: "flow"}
	observation := session.WorkflowExecutionObservation{
		Run:       api.WorkflowRun{ID: "flow", WorkflowID: "release", SessionID: "root"},
		Execution: session.ExecutionObservation{SessionID: "root", Settled: true},
	}
	testutil.FailErr(t, "validate workflow observation", validateWorkflowObservation(observation, result))
	for _, mutate := range []func(*session.WorkflowExecutionObservation){
		func(o *session.WorkflowExecutionObservation) { o.Run.ID = "foreign" },
		func(o *session.WorkflowExecutionObservation) { o.Run.SessionID = "foreign" },
		func(o *session.WorkflowExecutionObservation) {
			o.Execution.Blockers = []session.ExecutionBlocker{{Kind: "continuation"}}
		},
	} {
		changed := observation
		mutate(&changed)
		if err := validateWorkflowObservation(changed, result); err == nil {
			t.Fatal("invalid workflow observation accepted")
		}
	}
}
