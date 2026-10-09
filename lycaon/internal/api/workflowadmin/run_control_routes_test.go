package workflowadmin_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowRunReadsAnswerStoredRuns(t *testing.T) {
	f := newRoutesFixture(t, nil)
	run := f.seedRun(t, "")

	got := decodeOK[wire.WorkflowRun](t, f.do(t, http.MethodGet, "/v1/workflow-runs/"+run.ID, "", ""), http.StatusOK)
	if got.ID != run.ID || got.SessionID != run.SessionID || got.Status != wire.WorkflowRunStatusRunning {
		t.Fatalf("run = %+v", got)
	}
	active := decodeOK[wire.ActiveWorkflowRunResponse](t, f.do(t, http.MethodGet, "/v1/sessions/"+run.SessionID+"/workflow-runs/active", "", ""), http.StatusOK)
	if active.Run == nil || active.Run.ID != run.ID {
		t.Fatalf("active run = %+v", active.Run)
	}
	idle := f.createSession(t)
	none := decodeOK[wire.ActiveWorkflowRunResponse](t, f.do(t, http.MethodGet, "/v1/sessions/"+idle.ID+"/workflow-runs/active", "", ""), http.StatusOK)
	if none.Run != nil {
		t.Fatalf("idle session active run = %+v", none.Run)
	}

	wantError(t, f.do(t, http.MethodGet, "/v1/workflow-runs/"+uuid.NewString(), "", ""), wire.ApiErrorCodeWorkflowRunNotFound)
	wantError(t, f.do(t, http.MethodGet, "/v1/sessions/"+uuid.NewString()+"/workflow-runs/active", "", ""), wire.ApiErrorCodeSessionNotFound)
}

func TestWorkflowRunReportRequiresASettledRun(t *testing.T) {
	f := newRoutesFixture(t, nil)
	run := f.seedRun(t, "")

	wantError(t, f.do(t, http.MethodGet, "/v1/workflow-runs/"+uuid.NewString()+"/report", "", ""), wire.ApiErrorCodeWorkflowRunNotFound)
	wantError(t, f.do(t, http.MethodGet, "/v1/workflow-runs/"+run.ID+"/report", "", ""), wire.ApiErrorCodeReportNotFound)
}

func TestListSessionWorkflowRunsFiltersByStatus(t *testing.T) {
	f := newRoutesFixture(t, nil)
	run := f.seedRun(t, "")
	base := "/v1/sessions/" + run.SessionID + "/workflow-runs"

	page := decodeOK[wire.WorkflowRunPage](t, f.do(t, http.MethodGet, base+"?status=running", "", ""), http.StatusOK)
	if len(page.Runs) != 1 || page.Runs[0].ID != run.ID {
		t.Fatalf("running page = %+v", page.Runs)
	}
	empty := decodeOK[wire.WorkflowRunPage](t, f.do(t, http.MethodGet, base+"?status=complete,canceled", "", ""), http.StatusOK)
	if empty.Runs == nil || len(empty.Runs) != 0 {
		t.Fatalf("terminal page = %#v", empty.Runs)
	}

	wantError(t, f.do(t, http.MethodGet, base+"?status=running,bogus", "", ""), wire.ApiErrorCodeInvalidQuery)
	wantError(t, f.do(t, http.MethodGet, base+"?limit=0", "", ""), wire.ApiErrorCodeInvalidQuery)
	wantError(t, f.do(t, http.MethodGet, "/v1/sessions/"+uuid.NewString()+"/workflow-runs", "", ""), wire.ApiErrorCodeSessionNotFound)
}

func TestListWorkflowsScopesDiscovery(t *testing.T) {
	f := newRoutesFixture(t, nil)
	sess := f.createSession(t)

	for _, query := range []string{"", "?project_id=" + f.project.ID, "?session_id=" + sess.ID} {
		list := decodeOK[wire.WorkflowListResponse](t, f.do(t, http.MethodGet, "/v1/workflows"+query, "", ""), http.StatusOK)
		if list.Workflows == nil {
			t.Fatalf("workflows%s = nil", query)
		}
	}
	wantError(t, f.do(t, http.MethodGet, "/v1/workflows?project_id="+uuid.NewString(), "", ""), wire.ApiErrorCodeProjectNotFound)
	wantError(t, f.do(t, http.MethodGet, "/v1/workflows?session_id="+uuid.NewString(), "", ""), wire.ApiErrorCodeSessionNotFound)
}

func TestStartWorkflowRunRefusesInvalidRequests(t *testing.T) {
	f := newRoutesFixture(t, nil)
	sess := f.createSession(t)
	path := "/v1/sessions/" + sess.ID + "/workflow-runs"
	op := uuid.NewString()

	tests := []struct {
		name string
		path string
		body string
		want wire.ApiErrorCode
	}{
		{name: "malformed body", path: path, body: `{`, want: wire.ApiErrorCodeInvalidJson},
		{name: "operation not a uuid", path: path, body: `{"operation_id":"op","workflow_id":"w","workflow_version":"1.0.0"}`, want: wire.ApiErrorCodeInvalidRequest},
		{name: "replacement without revision", path: path, body: fmt.Sprintf(`{"operation_id":%q,"workflow_id":"w","workflow_version":"1.0.0","replace_run_id":"r"}`, op), want: wire.ApiErrorCodeInvalidWorkflowReplacementTarget},
		{name: "missing workflow", path: path, body: fmt.Sprintf(`{"operation_id":%q,"workflow_version":"1.0.0"}`, op), want: wire.ApiErrorCodeInvalidRequest},
		{name: "unknown session", path: "/v1/sessions/" + uuid.NewString() + "/workflow-runs", body: fmt.Sprintf(`{"operation_id":%q,"workflow_id":"w","workflow_version":"1.0.0"}`, op), want: wire.ApiErrorCodeSessionNotFound},
		{name: "unknown workflow", path: path, body: fmt.Sprintf(`{"operation_id":%q,"workflow_id":"no-such-workflow","workflow_version":"1.0.0"}`, op), want: wire.ApiErrorCodeWorkflowNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantError(t, f.postJSON(t, tt.path, tt.body), tt.want)
		})
	}
}

func TestWorkflowRunControlsRefuseStaleOrMissingTargets(t *testing.T) {
	f := newRoutesFixture(t, nil)
	run := f.seedRun(t, "")
	unknown := "/v1/workflow-runs/" + uuid.NewString()
	known := "/v1/workflow-runs/" + run.ID
	stale := fmt.Sprintf(`{"expected_revision":%d}`, run.Revision+5)

	tests := []struct {
		name string
		path string
		body string
		want wire.ApiErrorCode
	}{
		{name: "exit malformed", path: known + "/exit", body: `{`, want: wire.ApiErrorCodeInvalidJson},
		{name: "exit without revision", path: known + "/exit", body: `{}`, want: wire.ApiErrorCodeInvalidWorkflowTarget},
		{name: "exit unknown run", path: unknown + "/exit", body: `{"expected_revision":1}`, want: wire.ApiErrorCodeWorkflowRunNotFound},
		{name: "pause malformed", path: known + "/pause", body: `{`, want: wire.ApiErrorCodeInvalidJson},
		{name: "pause without revision", path: known + "/pause", body: `{}`, want: wire.ApiErrorCodeInvalidWorkflowRevision},
		{name: "pause stale revision", path: known + "/pause", body: stale, want: wire.ApiErrorCodeWorkflowRevisionConflict},
		{name: "resume unknown run", path: unknown + "/resume", body: `{"expected_revision":1}`, want: wire.ApiErrorCodeWorkflowRunNotFound},
		{name: "cancel unknown run", path: unknown + "/cancel", body: `{"expected_revision":1}`, want: wire.ApiErrorCodeWorkflowRunNotFound},
		{name: "advance malformed", path: known + "/advance", body: `{`, want: wire.ApiErrorCodeInvalidJson},
		{name: "advance without revision", path: known + "/advance", body: `{}`, want: wire.ApiErrorCodeInvalidWorkflowRevision},
		{name: "advance unknown run", path: unknown + "/advance", body: `{"expected_revision":1}`, want: wire.ApiErrorCodeWorkflowRunNotFound},
		{name: "transition malformed", path: known + "/transitions/next", body: `{`, want: wire.ApiErrorCodeInvalidJson},
		{name: "transition without revision", path: known + "/transitions/next", body: `{}`, want: wire.ApiErrorCodeInvalidWorkflowRevision},
		{name: "transition unknown run", path: unknown + "/transitions/next", body: `{"expected_revision":1}`, want: wire.ApiErrorCodeWorkflowRunNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantError(t, f.postJSON(t, tt.path, tt.body), tt.want)
		})
	}

	got, err := f.runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "reload run", err)
	if got.Status != wire.WorkflowRunStatusRunning || got.Revision != run.Revision {
		t.Fatalf("refused controls changed the run: status %s revision %d", got.Status, got.Revision)
	}
}

func TestWorkflowHumanInputRefusesInvalidRequests(t *testing.T) {
	f := newRoutesFixture(t, nil)
	run := f.seedRun(t, "")
	routes := []string{"/decisions/choose", "/feedback/review", "/feedback/review/secret"}

	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			known := "/v1/workflow-runs/" + run.ID + route
			wantError(t, f.postJSON(t, "/v1/workflow-runs/"+uuid.NewString()+route, `{"expected_revision":1}`), wire.ApiErrorCodeWorkflowRunNotFound)
			wantError(t, f.postJSON(t, known, `{`), wire.ApiErrorCodeInvalidJson)
			wantError(t, f.postJSON(t, known, `{}`), wire.ApiErrorCodeInvalidWorkflowRevision)
		})
	}
}

func TestOrchestratedTopologySkipsRunsWithoutTopology(t *testing.T) {
	orchestrator := &recordingOrchestrator{}
	f := newRoutesFixture(t, orchestrator)
	run := f.seedRun(t, "")

	testutil.FailErr(t, "recover topologies", f.handler.Topology.RecoverOrchestratedTopologies(t.Context()))
	f.handler.Topology.StartOrchestratedTopologyForRun(t.Context(), run.SessionID, nil)
	if n := orchestrator.started(); n != 0 {
		t.Fatalf("settlement started %d times for a workflow without a topology", n)
	}

	idle := newRoutesFixture(t, nil)
	testutil.FailErr(t, "recover without orchestrator", idle.handler.Topology.RecoverOrchestratedTopologies(t.Context()))
}
