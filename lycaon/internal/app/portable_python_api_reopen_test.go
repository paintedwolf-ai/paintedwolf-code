package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/mcp/mcptest"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestPortablePythonDenFilesAndRetainedOutputAPIReopen exercises the real
// production HTTP/API endpoints wired in app.Build:
//   - GET /v1/sessions/{id}/program-receipts/{execution_id} (Den activity + diffs + refusals + admissions)
//   - GET /v1/sessions/{id}/artifacts/{artifact_id} (retained overflow stdout)
//   - GET /v1/projects/{id}/source (Files content after reopen)
//
// It verifies live inspection, full server close & reopen from disk on the same
// data dir, and cross-session + cross-worker 403 Forbidden isolation at the
// exact HTTP/API boundary.
func TestPortablePythonDenFilesAndRetainedOutputAPIReopen(t *testing.T) {
	ctx := t.Context()
	cfgDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "app-python-api.db")
	mcpOverride := filepath.Join(t.TempDir(), "mcp-override.yaml")

	existingEngineRoot := strings.TrimSpace(os.Getenv(configlayout.EnvEngineRoot))
	hasStaged := existingEngineRoot != "" && isRegularFile(filepath.Join(existingEngineRoot, "portable-python", "image", "image-manifest.json"))
	if !hasStaged {
		sourceImg := strings.TrimSpace(os.Getenv(portablepython.EnvImageDir))
		if sourceImg == "" {
			t.Skip("neither staged LYCAON_ENGINE_ROOT nor PW_PORTABLE_PYTHON_IMAGE_DIR is set")
		}
		engineRoot := t.TempDir()
		stagedParent := filepath.Join(engineRoot, "portable-python")
		testutil.FailErr(t, "mkdir staged engine-root", os.MkdirAll(stagedParent, 0o755))
		testutil.FailErr(t, "symlink portable python image", os.Symlink(sourceImg, filepath.Join(stagedParent, "image")))
		t.Setenv(configlayout.EnvEngineRoot, engineRoot)
	}
	t.Setenv(portablepython.EnvImageDir, "")
	t.Setenv(configdir.EnvConfigDir, cfgDir)
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "py-api-reopen-token")
	configtest.Overlay(t, map[config.Rel]string{
		config.DistroMCP: "providers:\n  - id: svca\n    command: \"true\"\n    args: []\n    enabled: false\n",
	})

	buildApp := func() *ServeApp {
		cfg := configuration.Config{
			DBPath:                    dbPath,
			ListenAddr:                "127.0.0.1:0",
			ConfigRoot:                configlayout.FindModuleRoot(),
			TestMCPConnector:          &mcptest.Connector{Tools: map[string][]*sdkmcp.Tool{"svca": {{Name: "do"}}}},
			TestMCPGlobalOverridePath: mcpOverride,
		}
		appInst, err := Build(ctx, cfg)
		testutil.FailErr(t, "app.Build", err)
		if appInst.Commands == nil || appInst.Commands.PortablePython() == nil {
			t.Fatal("expected app.Build to auto-wire Portable Python service")
		}
		return appInst
	}

	wsDir := t.TempDir()
	wsDir, err := filepath.EvalSymlinks(wsDir)
	testutil.FailErr(t, "eval symlinks wsDir", err)
	testutil.FailErr(t, "seed input.txt", os.WriteFile(filepath.Join(wsDir, "input.txt"), []byte("hello-api\n"), 0o644))

	app1 := buildApp()
	ts1 := httptest.NewServer(app1.Server)

	// 1. Create project, owning coordinator session, unrelated coordinator session,
	// and an unrelated child worker session under the owning coordinator.
	proj := mustCreateProjectHTTP(t, ts1.URL, wsDir)
	ownerSess := mustCreateSessionHTTP(t, ts1.URL, proj.ID)
	otherSess := mustCreateSessionHTTP(t, ts1.URL, proj.ID)
	otherWorkerSess, err := app1.Server.Admin.SessionAdmin.Transcript.SessionStore.CreateChild(ctx, &ownerSess, wire.SpawnChildRequest{
		AgentType: "other-worker",
		Prompt:    "unrelated worker",
	})
	testutil.FailErr(t, "create unrelated worker child session", err)

	// 2. Execute Python read + workspace write + >65536B overflow stdout, and a
	// denied protected write through the real registered `python` tool.
	rootID := proj.Roots[0].ID
	mkTC := func(callID string) tools.ToolContext {
		return tools.ToolContext{
			Identity: tools.InvocationIdentity{
				ProjectID:  proj.ID,
				SessionID:  ownerSess.ID,
				Agent:      "implement",
				ToolCallID: callID,
			},
			Source: tools.InvocationSource{
				SourceWorkspaceKind: wire.SourceWorkspaceKindProject,
				ActiveRootID:        rootID,
				SourceLedger:        app1.Server.Sources.Workspace.SourceLedger,
				Roots: []projectroot.RootRef{{
					ID:    rootID,
					Label: "main",
					Path:  wsDir,
				}},
			},
		}
	}

	writeResp := runPythonToolHTTPTest(t, app1, map[string]any{
		"code": `txt = open("input.txt").read().strip(); open("out.txt", "w").write(txt + "-published\n"); print("R" * 80000)`,
	}, mkTC("call-api-write"))
	if !writeResp.CleanCompletion || !writeResp.StdoutTruncated || writeResp.RetainedStdoutArtifactID == "" {
		t.Fatalf("write+overflow run failed: %+v", writeResp)
	}

	denyResp := runPythonToolHTTPTest(t, app1, map[string]any{
		"code": `open(".git/config", "w").write("bad")`,
	}, mkTC("call-api-deny"))
	if denyResp.CleanCompletion || denyResp.Stop == nil || denyResp.Stop.Code != pythonruntime.StopEffectNotAllowed {
		t.Fatalf("expected protected write refusal, got %+v", denyResp)
	}

	// 3. Verify live HTTP/API access before reopen.
	verifyAuthorizedPythonAPI(t, ts1.URL, proj.ID, ownerSess.ID, writeResp, denyResp)
	verifyForbiddenPythonAPI(t, ts1.URL, otherSess.ID, writeResp.ExecutionID, writeResp.RetainedStdoutArtifactID)
	verifyForbiddenPythonAPI(t, ts1.URL, otherWorkerSess.ID, writeResp.ExecutionID, writeResp.RetainedStdoutArtifactID)

	// 4. Close the server/app completely and reopen a fresh app.Build instance
	// against the exact same on-disk cfgDir and dbPath.
	ts1.Close()
	testutil.FailErr(t, "close app1", app1.Close())

	app2 := buildApp()
	defer func() { _ = app2.Close() }()
	ts2 := httptest.NewServer(app2.Server)
	defer ts2.Close()

	// 5. After reopen: owning session STILL opens Python activity, reads retained
	// output, and sees the published file via the real Files API; unrelated
	// session and unrelated worker are STILL denied (403 Forbidden).
	verifyAuthorizedPythonAPI(t, ts2.URL, proj.ID, ownerSess.ID, writeResp, denyResp)
	verifyForbiddenPythonAPI(t, ts2.URL, otherSess.ID, writeResp.ExecutionID, writeResp.RetainedStdoutArtifactID)
	verifyForbiddenPythonAPI(t, ts2.URL, otherWorkerSess.ID, writeResp.ExecutionID, writeResp.RetainedStdoutArtifactID)
}

func verifyAuthorizedPythonAPI(
	t *testing.T,
	baseURL, projectID, sessionID string,
	writeResp, denyResp portablepython.ToolResponse,
) {
	t.Helper()

	// 1. Den activity receipt for write execution: PublishedDiffs + persisted admissions.
	status, body := doAuthedHTTP(t, http.MethodGet, baseURL+"/v1/sessions/"+sessionID+"/program-receipts/"+writeResp.ExecutionID, nil)
	if status != http.StatusOK {
		t.Fatalf("GET write python activity status = %d, want 200 (body=%s)", status, string(body))
	}
	var writePage wire.CommandProgramReceiptPage
	testutil.FailErr(t, "decode write program receipt page", json.Unmarshal(body, &writePage))
	if writePage.Program.ID != writeResp.ExecutionID || writePage.Program.State != "completed" {
		t.Fatalf("unexpected write program header: %+v", writePage.Program)
	}
	sawPub := false
	for _, act := range writePage.Activities {
		if act.Group == "workspace_write" && act.Label == string(pythonevidence.AttributionPythonWorkspacePublication) && act.Subject == "out.txt" {
			sawPub = true
		}
	}
	if !sawPub {
		t.Fatalf("expected workspace_write publication activity for out.txt, got %+v", writePage.Activities)
	}
	if len(writePage.Executions) == 0 {
		t.Fatalf("expected persisted admission executions on write activity page")
	}

	// 2. Den activity receipt for denied execution: RefusedChildren.
	status, body = doAuthedHTTP(t, http.MethodGet, baseURL+"/v1/sessions/"+sessionID+"/program-receipts/"+denyResp.ExecutionID, nil)
	if status != http.StatusOK {
		t.Fatalf("GET deny python activity status = %d, want 200 (body=%s)", status, string(body))
	}
	var denyPage wire.CommandProgramReceiptPage
	testutil.FailErr(t, "decode deny program receipt page", json.Unmarshal(body, &denyPage))
	sawRefusal := false
	for _, act := range denyPage.Activities {
		if act.Group == "refusal" && act.Label == string(pythonruntime.StopEffectNotAllowed) {
			sawRefusal = true
		}
	}
	if !sawRefusal {
		t.Fatalf("expected refusal activity on deny page, got %+v", denyPage.Activities)
	}

	// 3. Retained overflow stdout via GET /v1/sessions/{id}/artifacts/{artifact_id}.
	status, retained := doAuthedHTTP(t, http.MethodGet, baseURL+"/v1/sessions/"+sessionID+"/artifacts/"+writeResp.RetainedStdoutArtifactID, nil)
	if status != http.StatusOK {
		t.Fatalf("GET retained python artifact status = %d, want 200 (body=%s)", status, string(retained))
	}
	if len(retained) < 80000 || !strings.Contains(string(retained), strings.Repeat("R", 1024)) {
		t.Fatalf("retained artifact bytes = %d, want >= 80000 R's", len(retained))
	}

	// 4. Published workspace file via real Files API (GET /v1/projects/{id}/source).
	status, srcBody := doAuthedHTTP(t, http.MethodGet, baseURL+"/v1/projects/"+projectID+"/source?path="+url.QueryEscape("out.txt"), nil)
	if status != http.StatusOK {
		t.Fatalf("GET project source out.txt status = %d, want 200 (body=%s)", status, string(srcBody))
	}
	if !strings.Contains(string(srcBody), "hello-api-published") {
		t.Fatalf("Files API response for out.txt missing published content: %s", string(srcBody))
	}
}

func verifyForbiddenPythonAPI(t *testing.T, baseURL, foreignSessionID, executionID, artifactID string) {
	t.Helper()

	status, body := doAuthedHTTP(t, http.MethodGet, baseURL+"/v1/sessions/"+foreignSessionID+"/program-receipts/"+executionID, nil)
	if status != http.StatusForbidden {
		t.Fatalf("foreign session %s GET python activity status = %d, want 403 (body=%s)", foreignSessionID, status, string(body))
	}
	if strings.Contains(string(body), "out.txt") || strings.Contains(string(body), "python_workspace_publication") {
		t.Fatalf("foreign session %s activity response leaked data: %s", foreignSessionID, string(body))
	}

	status, body = doAuthedHTTP(t, http.MethodGet, baseURL+"/v1/sessions/"+foreignSessionID+"/artifacts/"+artifactID, nil)
	if status != http.StatusForbidden {
		t.Fatalf("foreign session %s GET retained artifact status = %d, want 403 (body=%s)", foreignSessionID, status, string(body))
	}
	if strings.Contains(string(body), strings.Repeat("R", 64)) {
		t.Fatalf("foreign session %s retained response leaked bytes", foreignSessionID)
	}
}

func runPythonToolHTTPTest(t *testing.T, appInst *ServeApp, args map[string]any, tc tools.ToolContext) portablepython.ToolResponse {
	t.Helper()
	raw, err := appInst.ToolRegistry.Run(t.Context(), "python", args, tc)
	testutil.FailErr(t, "run registered python tool", err)
	var resp portablepython.ToolResponse
	testutil.FailErr(t, "decode ToolResponse", json.Unmarshal([]byte(raw), &resp))
	return resp
}

func mustCreateProjectHTTP(t *testing.T, baseURL, wsDir string) wire.Project {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"roots": []map[string]any{{"path": wsDir}},
	})
	testutil.FailErr(t, "marshal create project", err)
	status, body := doAuthedHTTP(t, http.MethodPost, baseURL+"/v1/projects", payload)
	if status != http.StatusCreated {
		t.Fatalf("POST /v1/projects status = %d, want 201 (body=%s)", status, string(body))
	}
	var proj wire.Project
	testutil.FailErr(t, "decode created project", json.Unmarshal(body, &proj))
	return proj
}

func mustCreateSessionHTTP(t *testing.T, baseURL, projectID string) wire.Session {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"project_id": projectID,
		"posture":    "spec",
	})
	testutil.FailErr(t, "marshal create session", err)
	status, body := doAuthedHTTP(t, http.MethodPost, baseURL+"/v1/sessions", payload)
	if status != http.StatusCreated && status != http.StatusAccepted {
		t.Fatalf("POST /v1/sessions status = %d, want 201 or 202 (body=%s)", status, string(body))
	}
	var sess wire.Session
	testutil.FailErr(t, "decode created session", json.Unmarshal(body, &sess))
	return sess
}

func doAuthedHTTP(t *testing.T, method, targetURL string, body []byte) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, targetURL, reader)
	testutil.FailErr(t, "new http request", err)
	req.Header.Set("Authorization", "Bearer py-api-reopen-token")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "do http request "+targetURL, err)
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	testutil.FailErr(t, "read response body", err)
	return resp.StatusCode, out
}

func isRegularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
