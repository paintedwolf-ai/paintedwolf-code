package security

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestIdempotencySweepE2E(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	projectDir := t.TempDir()

	t.Run("create project — duplicate root path on separate creates yields distinct projects", func(t *testing.T) {
		first := createAPIProjectAtPath(t, base, projectDir)
		second := createAPIProjectAtPath(t, base, projectDir)
		if first.ID == second.ID {
			t.Fatalf("expected distinct projects, got same id %q", first.ID)
		}
		if primaryRootPath(first) != primaryRootPath(second) {
			t.Fatalf("root path drifted: %q → %q", primaryRootPath(first), primaryRootPath(second))
		}
	})

	project := createAPIProjectAtPath(t, base, projectDir)
	testWorkflowCommandIdempotency(t, h, base, project.ID)

	t.Run("PUT project source — identical content twice converges on the same sha", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "idem.txt"), []byte("v1\n"), 0o644); err != nil {
			testutil.FailErr(t, "seed source", err)
		}
		proj := createAPIProjectAtPath(t, base, dir)
		read := openAPIGetJSON[wire.ProjectSourceReadResponse](t, base,
			"/v1/projects/"+proj.ID+"/source?path=idem.txt", nil, http.StatusOK)
		body := `{"path":"idem.txt","content":"v2\n","encoding":"` + string(read.Encoding) + `","base_sha256":"` + read.SHA256 + `"}`
		first := openAPIPutJSON[wire.ProjectSourceWriteResponse](t, base,
			"/v1/projects/"+proj.ID+"/source", nil, body, http.StatusOK)
		// The second save uses the stored content hash.
		body2 := `{"path":"idem.txt","content":"v2\n","encoding":"` + string(read.Encoding) + `","base_sha256":"` + first.SHA256 + `"}`
		second := openAPIPutJSON[wire.ProjectSourceWriteResponse](t, base,
			"/v1/projects/"+proj.ID+"/source", nil, body2, http.StatusOK)
		if first != second {
			t.Fatalf("source write drifted: %+v → %+v", first, second)
		}
	})

	t.Run("PATCH pricing settings — same body twice yields same response", func(t *testing.T) {
		body := `{"cost_tracking_enabled":false,"sources":[]}`
		first := settingsPatchJSON[wire.SettingsPricingResponse](t, base, "/v1/settings/pricing",
			body, http.StatusOK)
		second := settingsPatchJSON[wire.SettingsPricingResponse](t, base, "/v1/settings/pricing",
			body, http.StatusOK)
		if first.CostTrackingEnabled != second.CostTrackingEnabled ||
			len(first.Sources) != len(second.Sources) {
			t.Fatalf("pricing drifted: %+v → %+v", first, second)
		}
	})

	t.Run("PATCH provider — same body twice yields same canonical response", func(t *testing.T) {
		body := `{"base_url":"https://idem.example/v1","models":[{"id":"m1","input_per_1k_nano_usd":1000000}]}`
		_ = openAPIPostJSON[wire.ProviderMeta](t, base, "/v1/providers", nil,
			`{"id":"idem-test","base_url":"https://idem.example/v1"}`, http.StatusCreated)
		first := settingsPatchJSON[wire.ProviderMeta](t, base, "/v1/providers/idem-test",
			body, http.StatusOK)
		second := settingsPatchJSON[wire.ProviderMeta](t, base, "/v1/providers/idem-test",
			body, http.StatusOK)
		if first.BaseURL != second.BaseURL {
			t.Fatalf("base_url drifted: %q → %q", first.BaseURL, second.BaseURL)
		}
		if len(first.Models) != len(second.Models) {
			t.Fatalf("models count drifted: %d → %d", len(first.Models), len(second.Models))
		}
		for i := range first.Models {
			if !reflect.DeepEqual(first.Models[i], second.Models[i]) {
				t.Fatalf("model %d drifted: %+v → %+v", i, first.Models[i], second.Models[i])
			}
		}
	})

	t.Run("PATCH mcp provider — same enabled flag twice yields same response", func(t *testing.T) {
		list := journeyGetJSON[wire.McpProviderListResponse](t, base, "/v1/mcp/providers", http.StatusOK).Providers
		if len(list) == 0 {
			t.Fatal("expected a seeded MCP provider to toggle")
		}
		id := list[0].ID
		body := `{"enabled":true}`
		first := settingsPatchJSON[wire.McpProvider](t, base,
			"/v1/mcp/providers/"+id, body, http.StatusOK)
		second := settingsPatchJSON[wire.McpProvider](t, base,
			"/v1/mcp/providers/"+id, body, http.StatusOK)
		firstJSON, _ := json.Marshal(first)
		secondJSON, _ := json.Marshal(second)
		if string(firstJSON) != string(secondJSON) {
			t.Fatalf("MCP provider PATCH drifted:\n  first  = %s\n  second = %s",
				firstJSON, secondJSON)
		}
	})

	testSettingsWriteIdempotency(t, base, project.ID)
}

func testWorkflowCommandIdempotency(t *testing.T, h *wiring.Harness, base, projectID string) {
	t.Helper()
	cases := []struct {
		name       string
		command    string
		wantStatus wire.WorkflowRunStatus
		seedPlan   bool
	}{
		{"pause", "pause", wire.WorkflowRunStatusPaused, false},
		{"resume", "resume", wire.WorkflowRunStatusRunning, false},
		{"cancel", "cancel", wire.WorkflowRunStatusCanceled, true},
	}
	for _, tc := range cases {
		t.Run("workflow "+tc.name, func(t *testing.T) {
			sess := journeyPostJSON[wire.Session](t, base, "/v1/sessions",
				`{"project_id":"`+projectID+`","posture":"spec"}`, http.StatusAccepted)
			run := journeyPostJSON[wire.WorkflowRun](t, base,
				"/v1/sessions/"+sess.ID+"/workflow-runs",
				workflowStartJSON(t, base, sess.ID, map[string]any{"workflow_id": "plan", "workflow_version": "1.0.0"}), http.StatusCreated)
			if tc.seedPlan {
				seedPlanStub(t, h.Workflows.Blueprints, run.ProjectID, run.BlueprintPath)
			}

			path := "/v1/workflow-runs/" + run.ID + "/" + tc.command
			first := journeyPostJSON[wire.WorkflowRun](t, base,
				path, workflowCommandJSON(t, base, run.ID, nil), http.StatusOK)
			if first.Status != tc.wantStatus {
				t.Fatalf("first %s status = %q, want %q", tc.command, first.Status, tc.wantStatus)
			}
			if tc.command == "cancel" && first.CompletedAt == nil {
				t.Fatal("first cancel must set completed_at")
			}

			second := journeyPostJSON[wire.WorkflowRun](t, base,
				path, workflowCommandJSON(t, base, run.ID, nil), http.StatusOK)
			if !second.UpdatedAt.Equal(first.UpdatedAt) {
				t.Fatalf("idempotent %s bumped updated_at: was %s now %s",
					tc.command, first.UpdatedAt, second.UpdatedAt)
			}
			if tc.command == "cancel" {
				if second.CompletedAt == nil || !second.CompletedAt.Equal(*first.CompletedAt) {
					t.Fatalf("idempotent cancel changed completed_at: was %v now %v",
						first.CompletedAt, second.CompletedAt)
				}
				return
			}
			exitPath, exitBody := workflowExitCall(t, base, sess.ID, "")
			_ = journeyPostJSON[wire.WorkflowRun](t, base, exitPath, exitBody, http.StatusOK)
		})
	}
}

// testSettingsWriteIdempotency replays each settings write and requires a stable answer.
func testSettingsWriteIdempotency(t *testing.T, base, projectID string) {
	t.Helper()
	policyProvider, _ := seedTestProvider(t, base, false)
	modelRef := wire.ModelRefDTO{ProviderID: policyProvider, Model: testProviderModel}
	modelPolicyBody, _ := json.Marshal(wire.ModelPolicy{
		Coordinator: &modelRef,
		Lite:        &modelRef,
		AgentPool: wire.AgentPoolDTO{
			Selection: "round_robin",
			Models:    []wire.ModelRefDTO{modelRef},
		},
	})

	idempotentWrites := []struct {
		name  string
		path  string
		body  string
		write func(t *testing.T, base, path, body string)
	}{
		{
			"PATCH model-policy — same body twice yields same response",
			"/v1/settings/model-policy",
			string(modelPolicyBody),
			idempotentTypedPatch[wire.ModelPolicy],
		},
		{
			"PATCH approval posture — same body twice yields same config",
			"/v1/settings/approvals",
			`{"rules":[],"approval_posture":"strict"}`,
			idempotentTypedPatch[wire.ApprovalConfigResponse],
		},
		{
			"PATCH approvals — same rules twice yields same merged config",
			"/v1/settings/approvals",
			`{"rules":[{"category":"tool","pattern":"write","effect":"ask"}]}`,
			idempotentTypedPatch[wire.ApprovalConfigResponse],
		},
		{
			"PATCH capability access — same exact rule twice yields same snapshot",
			"/v1/host-resources/docker",
			`{"access":"ask"}`,
			idempotentTypedPatch[wire.HostResourcesResponse],
		},
		{
			"PATCH limits — same body twice yields same response",
			"/v1/settings/limits",
			`{"max_iterations":12,"overlay_promote_max_iterations":30,"max_tool_result_bytes":65536,"llm_turn_timeout_ms":3600000,"coordinator_host_turn_timeout_ms":3600000,"coordinator_max_sleep_ms":3600000,"await_parent_workers_timeout_ms":3600000}`,
			idempotentTypedPatch[wire.SettingsLimitsResponse],
		},
		{
			"PATCH review — same body twice yields same response",
			"/v1/settings/review",
			`{"review_paths":[{"tool":"write","path":"docs/**"}]}`,
			idempotentTypedPatch[wire.ReviewSettingsResponse],
		},
		{
			"PATCH verify — same body twice yields same response",
			"/v1/settings/verify?project_id=" + projectID,
			`{"test":"./task check"}`,
			idempotentTypedPatch[wire.VerifySettingsResponse],
		},
		{
			// Partial patch requires valid policy fields.
			"PATCH security-scanners — same body twice yields same response",
			"/v1/settings/security-scanners",
			`{"enabled":true,"landed_change_scope":"path_scoped","source_verify":"stat"}`,
			idempotentTypedPatch[wire.SecurityScannersSettingsResponse],
		},
		{
			"PATCH project-trust settings — same body twice yields same response",
			"/v1/settings/project-trust",
			`{"enabled":{"extension_config":true,"extension_suggestions":true}}`,
			idempotentTypedPatch[wire.TrustSettingsResponse],
		},
		{
			// The selected scanner remains the sole slot occupant.
			"PUT scanner slot — same body twice yields same catalog",
			"/v1/scanners/slots/sast",
			`{"scanner_id":"lycaon-sast"}`,
			idempotentTypedPut[wire.ScannerListResponse],
		},
		{
			"PUT web-research credential — same body twice yields same response",
			"/v1/web-research/providers/brave/credential",
			`{"api_key":"brave-key-12345"}`,
			idempotentTypedPut[wire.WebResearchProviderMeta],
		},
		{
			// Pack ids use one encoded path segment.
			"PATCH extension pack enabled — same body twice yields same desired state",
			"/v1/extensions/packs/painted-wolf%2Fplan",
			`{"enabled":true}`,
			idempotentExtensionPatch,
		},
		{
			"PATCH extension unit disabled — same body twice yields same desired state",
			"/v1/extensions/units/workflows%2Fplan?scope=device",
			`{"enabled":false}`,
			idempotentExtensionPatch,
		},
		{
			"PATCH extension unit own — same body twice yields same desired state",
			"/v1/extensions/units/workflows%2Fplan",
			`{"own_pack_id":"painted-wolf/plan"}`,
			idempotentExtensionPatch,
		},
	}
	for _, row := range idempotentWrites {
		t.Run(row.name, func(t *testing.T) {
			row.write(t, base, row.path, row.body)
		})
	}
}

// Extension mutations advance revisions, so idempotency compares desired state.
func idempotentExtensionPatch(t *testing.T, base, path, body string) {
	t.Helper()
	revision := openAPIGetJSON[wire.ExtensionsCatalogView](t, base,
		"/v1/extensions", nil, http.StatusOK).Revision
	first := settingsPatchJSON[wire.ExtensionMutationResponse](t, base,
		path, withExpectedRevision(t, body, revision), http.StatusOK)
	second := settingsPatchJSON[wire.ExtensionMutationResponse](t, base,
		path, withExpectedRevision(t, body, first.View.Revision), http.StatusOK)
	firstJSON, _ := json.Marshal(first.View.Desired)
	secondJSON, _ := json.Marshal(second.View.Desired)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("%s desired state drifted:\n  first  = %s\n  second = %s", path, firstJSON, secondJSON)
	}
	if first.View.DesiredPath != second.View.DesiredPath {
		t.Fatalf("%s desired_path drifted: %q → %q", path, first.View.DesiredPath, second.View.DesiredPath)
	}
}

// Flag mutations carry the observed revision in the body.
func withExpectedRevision(t *testing.T, body, revision string) string {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		testutil.FailErr(t, "decode extension mutation body", err)
	}
	fields["expected_revision"] = revision
	raw, err := json.Marshal(fields)
	if err != nil {
		testutil.FailErr(t, "encode extension mutation body", err)
	}
	return string(raw)
}

// idempotentTypedPut requires byte-stable typed responses.
func idempotentTypedPut[T any](t *testing.T, base, path, body string) {
	t.Helper()
	first := settingsPutJSON[T](t, base, path, body, http.StatusOK)
	second := settingsPutJSON[T](t, base, path, body, http.StatusOK)
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("%s PUT drifted:\n  first  = %s\n  second = %s", path, firstJSON, secondJSON)
	}
}

// idempotentTypedPatch requires byte-stable typed responses.
func idempotentTypedPatch[T any](t *testing.T, base, path, body string) {
	t.Helper()
	first := settingsPatchJSON[T](t, base, path, body, http.StatusOK)
	second := settingsPatchJSON[T](t, base, path, body, http.StatusOK)
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("%s PATCH drifted:\n  first  = %s\n  second = %s", path, firstJSON, secondJSON)
	}
}

func TestOperationIDConflictSweepE2E(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	projectDir := t.TempDir()
	for _, name := range []string{"conflict.txt", "rename-from.txt", "copy-from.txt", "trash-me.txt", "trash-other.txt"} {
		if err := os.WriteFile(filepath.Join(projectDir, name), []byte("v1\n"), 0o644); err != nil {
			testutil.FailErr(t, "seed source", err)
		}
	}
	proj := createAPIProjectAtPath(t, base, projectDir)
	sess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+proj.ID+`","posture":"spec"}`, http.StatusAccepted)

	read := openAPIGetJSON[wire.ProjectSourceReadResponse](t, base,
		"/v1/projects/"+proj.ID+"/source?path=conflict.txt", nil, http.StatusOK)

	t.Run("POST prompts", func(t *testing.T) {
		op := uuid.NewString()
		assertOperationConflict(t, base, http.MethodPost,
			"/v1/sessions/"+sess.ID+"/prompts", http.StatusAccepted,
			`{"operation_id":"`+op+`","text":"first prose"}`,
			`{"operation_id":"`+op+`","text":"different prose"}`)
	})

	t.Run("POST workflow-runs", func(t *testing.T) {
		workflowSession := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
			`{"project_id":"`+proj.ID+`","posture":"spec"}`, http.StatusAccepted)
		op := uuid.NewString()
		start := func(blueprintTitle string) string {
			return workflowStartJSON(t, base, workflowSession.ID, map[string]any{
				"operation_id": op, "workflow_id": "plan", "workflow_version": "1.0.0", "blueprint_title": blueprintTitle,
			})
		}
		assertOperationConflict(t, base, http.MethodPost,
			"/v1/sessions/"+workflowSession.ID+"/workflow-runs", http.StatusCreated,
			start("first-plan"), start("second-plan"))
		exitPath, exitBody := workflowExitCall(t, base, workflowSession.ID, "")
		_ = journeyPostJSON[wire.WorkflowRun](t, base, exitPath, exitBody, http.StatusOK)
	})

	t.Run("PUT source", func(t *testing.T) {
		op := uuid.NewString()
		write := func(content string) string {
			return fmt.Sprintf(`{"operation_id":%q,"path":"conflict.txt","content":%q,"encoding":%q,"base_sha256":%q}`,
				op, content, string(read.Encoding), read.SHA256)
		}
		assertOperationConflict(t, base, http.MethodPut,
			"/v1/projects/"+proj.ID+"/source", http.StatusOK, write("v2\n"), write("v3\n"))
	})

	t.Run("POST source create", func(t *testing.T) {
		op := uuid.NewString()
		create := func(path string) string {
			return fmt.Sprintf(`{"operation_id":%q,"path":%q,"kind":"file"}`, op, path)
		}
		assertSourceOperationConflict(t, base, proj.ID, http.MethodPost,
			"/v1/projects/"+proj.ID+"/source", http.StatusCreated,
			create("created-a.txt"), create("created-b.txt"))
	})

	t.Run("POST source rename", func(t *testing.T) {
		op := uuid.NewString()
		rename := func(to string) string {
			return fmt.Sprintf(`{"operation_id":%q,"from":"rename-from.txt","to":%q}`, op, to)
		}
		assertSourceOperationConflict(t, base, proj.ID, http.MethodPost,
			"/v1/projects/"+proj.ID+"/source/rename", http.StatusOK,
			rename("renamed.txt"), rename("renamed-elsewhere.txt"))
	})

	t.Run("POST source copy", func(t *testing.T) {
		op := uuid.NewString()
		copyReq := func(to string) string {
			return fmt.Sprintf(`{"operation_id":%q,"from":"copy-from.txt","to":%q}`, op, to)
		}
		assertSourceOperationConflict(t, base, proj.ID, http.MethodPost,
			"/v1/projects/"+proj.ID+"/source/copy", http.StatusOK,
			copyReq("copied.txt"), copyReq("copied-elsewhere.txt"))
	})

	t.Run("DELETE source", func(t *testing.T) {
		op := uuid.NewString()
		del := func(path string) string {
			return "/v1/projects/" + proj.ID + "/source?path=" + path + "&operation_id=" + op
		}
		settleSourceRequest(t, base, proj.ID, http.MethodDelete, del("trash-me.txt"), "", http.StatusNoContent)
		assertConflictCode(t, base, http.MethodDelete, del("trash-other.txt"), "")
	})
}

// assertOperationConflict lands the first request so a receipt exists, then
// replays the same operation identity with mutated input.
func assertOperationConflict(t *testing.T, base, method, path string, firstStatus int, first, conflicting string) {
	t.Helper()
	openAPIDo(t, base, method, path, nil, first, firstStatus)
	assertConflictCode(t, base, method, path, conflicting)
}

// assertConflictCode requires the documented refusal for a conflicting replay.
func assertConflictCode(t *testing.T, base, method, path, body string) {
	t.Helper()
	raw := openAPIDo(t, base, method, path, nil, body, http.StatusConflict)
	var errResp wire.ErrorResponse
	if err := json.Unmarshal(raw, &errResp); err != nil {
		testutil.FailErr(t, "decode conflict response", err)
	}
	if errResp.Code != "idempotency_conflict" {
		t.Fatalf("%s %s conflict code = %q, want idempotency_conflict; body = %s", method, path, errResp.Code, raw)
	}
}

// Source mutations may return a durable operation before verification finishes.
func settleSourceRequest(t *testing.T, base, projectID, method, path, body string, wantStatus int) {
	t.Helper()
	res := validatedRequest(t, base, method, path, nil, body)
	AssertHTTPResponseMatchesOpenAPI(t, res.StatusCode, res.Header, res.Body, method, path, nil)
	if res.StatusCode == wantStatus {
		return
	}
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("source mutation status = %d, want %d or 202; body=%s", res.StatusCode, wantStatus, res.Body)
	}
	status := decodeOpenAPI[wire.SourceOperationStatus](t, method, path, res.Body)
	if status.OperationID == "" {
		t.Fatal("accepted source mutation lacks operation_id")
	}
	params := map[string]string{"id": projectID, "operation_id": status.OperationID}
	testutil.WaitFor(t, 30*time.Second, func() bool {
		status = openAPIGetJSON[wire.SourceOperationStatus](t, base, "/v1/projects/{id}/source/operations/{operation_id}", params, http.StatusOK)
		return status.Complete
	})
	if wantStatus >= 400 {
		if status.Error == nil {
			t.Fatalf("source mutation expected error status %d, got result: %v", wantStatus, status.Result)
		}
	} else if status.Error != nil {
		t.Fatalf("source mutation settled with error: %v", status.Error)
	}
}

func assertSourceOperationConflict(t *testing.T, base, projectID, method, path string, firstStatus int, first, conflicting string) {
	t.Helper()
	settleSourceRequest(t, base, projectID, method, path, first, firstStatus)
	assertConflictCode(t, base, method, path, conflicting)
}
