package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestAdvanceToolAndHTTPParity(t *testing.T) {
	parity := newAdvanceParityRig(t)

	t.Run("happy path returns matching run state", func(t *testing.T) {
		httpRun := parity.startRunHTTP(t, "parity-coord", "1.0.0")
		toolRun := parity.startRunTool(t, "parity-coord", "1.0.0")
		parity.seedReady(t, httpRun.ID)
		parity.seedReady(t, toolRun.ID)

		httpStatus, httpRunOut, httpErr := parity.advanceHTTP(t, httpRun.ID)
		toolStatus, toolResult, toolRunOut := parity.advanceTool(t, parity.toolSession.ID)

		if httpStatus != http.StatusOK {
			t.Fatalf("http advance status = %d body = %+v", httpStatus, httpErr)
		}
		if toolStatus != advanceToolStatusOK {
			t.Fatalf("tool advance status = %v result = %+v", toolStatus, toolResult)
		}
		if httpRunOut == nil || toolRunOut == nil {
			t.Fatalf("expected runs on both surfaces, got http=%v tool=%v", httpRunOut, toolRunOut)
		}
		if httpRunOut.CurrentPhase != toolRunOut.CurrentPhase {
			t.Fatalf("phase divergence: http=%q tool=%q", httpRunOut.CurrentPhase, toolRunOut.CurrentPhase)
		}
		if httpRunOut.WorkflowID != toolRunOut.WorkflowID {
			t.Fatalf("workflow id divergence: http=%q tool=%q", httpRunOut.WorkflowID, toolRunOut.WorkflowID)
		}
		if httpRunOut.Status != toolRunOut.Status {
			t.Fatalf("status divergence: http=%q tool=%q", httpRunOut.Status, toolRunOut.Status)
		}
	})

	t.Run("phase_gate_unmet exposes matching error shape", func(t *testing.T) {
		// Each case starts with independent workflow state.
		parity := newAdvanceParityRig(t)
		httpRun := parity.startRunHTTP(t, "parity-coord", "1.0.0")
		_ = parity.startRunTool(t, "parity-coord", "1.0.0")

		// With ready unseeded, the var_truthy:ready gate stays unmet for both.
		httpStatus, _, httpErr := parity.advanceHTTP(t, httpRun.ID)
		toolStatus, toolResult, _ := parity.advanceTool(t, parity.toolSession.ID)

		if httpStatus != http.StatusConflict {
			t.Fatalf("http status = %d want 409", httpStatus)
		}
		if toolStatus != advanceToolStatusErr {
			t.Fatalf("tool status = %v want err", toolStatus)
		}
		if httpErr.Code != "phase_gate_unmet" {
			t.Fatalf("http code = %q", httpErr.Code)
		}
		if toolResult.Error != "phase_gate_unmet" {
			t.Fatalf("tool error code = %q", toolResult.Error)
		}

		httpPhase, _ := httpErr.Details["phase"].(string)
		if httpPhase != toolResult.Phase {
			t.Fatalf("phase divergence: http=%q tool=%q", httpPhase, toolResult.Phase)
		}
		if httpPhase == "" {
			t.Fatal("http body missing phase detail")
		}
		httpFailedGate, _ := httpErr.Details["failed_gate"].(string)
		if httpFailedGate != toolResult.FailedGate {
			t.Fatalf("failed_gate divergence: http=%q tool=%q", httpFailedGate, toolResult.FailedGate)
		}
		httpReason, _ := httpErr.Details["reason"].(string)
		if httpReason != toolResult.Reason {
			t.Fatalf("reason divergence: http=%q tool=%q", httpReason, toolResult.Reason)
		}
		httpLeaves := stringSliceDetail(httpErr.Details["failed_leaves"])
		if !equalStringSlices(httpLeaves, toolResult.FailedLeaves) {
			t.Fatalf("failed_leaves divergence: http=%v tool=%v", httpLeaves, toolResult.FailedLeaves)
		}
		if len(toolResult.FailedLeaves) == 0 {
			t.Fatal("expected at least one failed leaf in tool result")
		}
	})

	t.Run("no active run yields parallel rejections", func(t *testing.T) {
		parity := newAdvanceParityRig(t)
		exitReq := workflowExitRequest(t, parity.srv, parity.toolSession.ID, "test")
		exitW := httptest.NewRecorder()
		parity.srv.ServeHTTP(exitW, exitReq)
		if exitW.Code != http.StatusOK {
			t.Fatalf("exit ambient status = %d body = %s", exitW.Code, exitW.Body.String())
		}
		active, err := parity.wfMgr.Store.Runs.ActiveBySession(t.Context(), parity.toolSession.ID)
		if err != nil || active != nil {
			t.Fatalf("ambient exit left active workflow: %+v; error=%v", active, err)
		}
		// No catalog run is started.
		httpStatus, _, httpErr := parity.advanceUnknownRunHTTP(t, "nonexistent-run-id")
		toolStatus, _, toolBareErr := parity.advanceToolRaw(t, parity.toolSession.ID)

		if httpStatus < 400 {
			t.Fatalf("http status = %d want >=400", httpStatus)
		}
		if toolStatus == advanceToolStatusOK {
			t.Fatal("tool should reject with no active run")
		}
		if httpErr.Code == "" {
			t.Fatal("http error code missing")
		}
		if toolBareErr == nil {
			t.Fatal("tool should return error for no active run")
		}
		if !strings.Contains(toolBareErr.Error(), "active") {
			t.Fatalf("tool error %q should mention no active run", toolBareErr.Error())
		}
	})

	t.Run("gate-unmet HTTP path queues KickGateBlocked nudge", func(t *testing.T) {
		// HTTP advance queues feedback; tool advance returns the gate details directly.
		parity := newAdvanceParityRig(t)
		httpRun := parity.startRunHTTP(t, "parity-coord", "1.0.0")
		// Holding the execution lane keeps the coordinator loop from consuming the
		// kick before it is read.
		finishExecution := parity.sessionMgr.BeginPromptExecutionForTest(t.Context(), parity.httpSession.ID)
		defer finishExecution()
		// Drain startup feedback to isolate the failed-advance event.
		parity.sessionMgr.ClearPendingKickForTest(parity.httpSession.ID)

		status, _, errBody := parity.advanceHTTP(t, httpRun.ID)
		if status != http.StatusConflict {
			t.Fatalf("status = %d want 409", status)
		}
		if errBody.Code != "phase_gate_unmet" {
			t.Fatalf("code = %q", errBody.Code)
		}
		kickID, ok := parity.sessionMgr.PendingKickIDForTest(parity.httpSession.ID)
		if !ok || kickID != anchor.InformRender(anchor.GateBlocked) {
			t.Fatalf("pending kick = %q, %v want %q", kickID, ok, anchor.InformRender(anchor.GateBlocked))
		}
	})
}

// --- helpers -----------------------------------------------------------------

type advanceToolStatus int

const (
	advanceToolStatusOK advanceToolStatus = iota
	advanceToolStatusErr
)

type advanceParityRig struct {
	srv          *api.Server
	wfMgr        *workflow.RunManager
	blueprintMgr *blueprint.Manager
	sessionMgr   *session.Manager
	registry     *tools.DefaultRegistry
	httpSession  wire.Session
	toolSession  wire.Session
	httpProject  string
	toolProject  string
}

// The fixture uses coordinator advance so both HTTP and tool entry points are available.
const parityCoordWorkflowYAML = `id: parity-coord
version: 1.0.0
trigger: /parity-coord
request:
  question: What should the parity run exercise?
initial_posture: build
agents:
  - { id: repo-researcher, tools: profile }
phases:
  - id: gate
    activity_label: Waiting on the parity gate
    complete_when: var_truthy:ready
    next: done
  - id: done
    activity_label: Parity run complete
    terminal: true
    complete_when: orchestration_complete
rules:
  - config/packs/painted-wolf/platform/host/posture-rules/coordinator.yaml
`

// The overlay loader discovers workflows in named subdirectories.
func installParityCoordWorkflow(t *testing.T, projectDir string) {
	t.Helper()
	dir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows", "parity-coord")
	testutil.FailErr(t, "mkdir project workflows", os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "write parity workflow overlay",
		os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte(parityCoordWorkflowYAML), 0o644))
}

func newAdvanceParityRig(t *testing.T) *advanceParityRig {
	t.Helper()
	h := wiring.BuildForTest(t)
	httpDir := t.TempDir()
	toolDir := t.TempDir()
	installParityCoordWorkflow(t, httpDir)
	installParityCoordWorkflow(t, toolDir)
	httpProj := createProjectHTTP(t, h.Server, httpDir)
	httpSession := createSessionForProjectHTTP(t, h.Server, httpProj.ID, wire.SessionPostureBuild)
	toolProj := createProjectHTTP(t, h.Server, toolDir)
	toolSession := createSessionForProjectHTTP(t, h.Server, toolProj.ID, wire.SessionPostureBuild)
	return &advanceParityRig{
		srv:          h.Server,
		wfMgr:        h.WorkflowMgr,
		blueprintMgr: h.BlueprintMgr,
		sessionMgr:   h.SessionMgr,
		registry:     h.ToolRegistry,
		httpSession:  httpSession,
		toolSession:  toolSession,
		httpProject:  httpSession.WorkspacePath,
		toolProject:  toolSession.WorkspacePath,
	}
}

func (p *advanceParityRig) startRunHTTP(t *testing.T, workflowID, version string) wire.WorkflowRun {
	t.Helper()
	return p.startRun(t, p.httpSession.ID, workflowID, version)
}

func (p *advanceParityRig) startRunTool(t *testing.T, workflowID, version string) wire.WorkflowRun {
	t.Helper()
	return p.startRun(t, p.toolSession.ID, workflowID, version)
}

func (p *advanceParityRig) startRun(t *testing.T, sessionID, workflowID, version string) wire.WorkflowRun {
	t.Helper()
	body := `{"workflow_id":"` + workflowID + `","workflow_version":"` + version + `","request":"Exercise the parity gate"}`
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sessionID+"/workflow-runs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	return run
}

func (p *advanceParityRig) advanceUnknownRunHTTP(t *testing.T, runID string) (int, *wire.WorkflowRun, wire.ErrorResponse) {
	t.Helper()
	return p.advanceHTTPBody(t, runID, unknownRunCommandBody(t, runID))
}

func (p *advanceParityRig) advanceHTTP(t *testing.T, runID string) (int, *wire.WorkflowRun, wire.ErrorResponse) {
	t.Helper()
	return p.advanceHTTPBody(t, runID, workflowCommandBody(t, p.srv, runID, nil))
}

func (p *advanceParityRig) advanceHTTPBody(t *testing.T, runID string, body *strings.Reader) (int, *wire.WorkflowRun, wire.ErrorResponse) {
	t.Helper()
	req := authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+runID+"/advance", body)
	w := httptest.NewRecorder()
	p.srv.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		var run wire.WorkflowRun
		if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
			t.Fatalf("decode run: %v body = %s", err, w.Body.String())
		}
		return w.Code, &run, wire.ErrorResponse{}
	}
	return w.Code, nil, decodeAPIError(t, w)
}

func (p *advanceParityRig) advanceTool(t *testing.T, sessionID string) (advanceToolStatus, workflowphases.AdvanceToolResult, *wire.WorkflowRun) {
	t.Helper()
	out, err := p.registry.Run(context.Background(), "workflow_advance", map[string]any{}, securityToolContext(sessionID, p.toolProject, "coordinator"))
	if err != nil {
		// Gate failures use the structured result error.
		t.Fatalf("tool advance returned bare error: %v", err)
	}
	var result workflowphases.AdvanceToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode tool result: %v body = %s", err, out)
	}
	status := advanceToolStatusOK
	if result.Error != "" {
		status = advanceToolStatusErr
	}
	return status, result, result.Run
}

func (p *advanceParityRig) advanceToolRaw(t *testing.T, sessionID string) (advanceToolStatus, workflowphases.AdvanceToolResult, error) {
	t.Helper()
	out, err := p.registry.Run(context.Background(), "workflow_advance", map[string]any{}, securityToolContext(sessionID, p.toolProject, "coordinator"))
	if err != nil {
		return advanceToolStatusErr, workflowphases.AdvanceToolResult{}, err
	}
	var result workflowphases.AdvanceToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode tool result: %v body = %s", err, out)
	}
	status := advanceToolStatusOK
	if result.Error != "" {
		status = advanceToolStatusErr
	}
	return status, result, nil
}

// seedReady satisfies the parity-coord gate (var_truthy:ready) for a run.
func (p *advanceParityRig) seedReady(t *testing.T, runID string) {
	t.Helper()
	ctx := context.Background()
	run, err := p.wfMgr.Store.Runs.Get(ctx, runID)
	testutil.FailErr(t, "get run", err)
	vars, err := p.wfMgr.Store.Runs.GetScaffoldVars(ctx, runID)
	testutil.FailErr(t, "get scaffold vars", err)
	vars = runstate.SetHostVar(vars, "ready", true)
	testutil.FailErr(t, "upsert scaffold state",
		p.wfMgr.Store.State.UpdateVars(ctx, run, "", vars))
}

func stringSliceDetail(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
