package contract

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	openapi "github.com/lycaon/lycaon/test/openapi"
)

func TestOpenAPIRoutesCoveredByStub(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	routes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	if len(routes) == 0 {
		t.Fatal("no routes parsed from openapi.yaml")
	}

	srv := NewStubServer()
	t.Cleanup(srv.Close)
	client := srv.Client()

	for _, route := range routes {
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			t.Parallel()
			url := stubURL(srv.URL, route.Method, route.Path)
			bodyJSON := "{}"
			if route.OperationID == "exportProjectFindings" {
				bodyJSON = `{"format":"sarif"}`
			}
			req, err := http.NewRequestWithContext(t.Context(), route.Method, url, strings.NewReader(bodyJSON))
			contractcheck.FailErr(t, "http.NewRequest failed", err)
			if route.Method == http.MethodPost || route.Method == http.MethodPut || route.Method == http.MethodPatch {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := client.Do(req)
			contractcheck.FailErr(t, "client.Do failed", err)
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				t.Fatalf("stub missing route %s %s (operationId=%s)", route.Method, route.Path, route.OperationID)
			}
			if resp.StatusCode >= 500 {
				t.Fatalf("stub returned %d for %s %s", resp.StatusCode, route.Method, route.Path)
			}
			if isIncrementalStreamRoute(route.Path) {
				return
			}
			body, err := io.ReadAll(resp.Body)
			contractcheck.FailErr(t, "io.ReadAll failed", err)
			if len(body) == 0 {
				return
			}
			ct := resp.Header.Get("Content-Type")
			if strings.Contains(ct, "text/event-stream") {
				return
			}
			pathParams := stubPathParams(route.Path)
			if err := openapi.ValidateResponse(context.Background(), route.Method, route.Path, pathParams, resp.StatusCode, resp.Header, body); err != nil {
				t.Fatalf("stub response schema: %v", err)
			}
		})
	}
}

func isIncrementalStreamRoute(path string) bool {
	return strings.HasSuffix(path, "/stream")
}

func stubPathParams(path string) map[string]string {
	params := map[string]string{
		"view_id":         fixtureSourceViewID,
		"presentation_id": fixtureSourcePresentationID,
		"interest_id":     fixtureSourceInterestID,
		"operation_id":    fixtureCheckpointID,
		"id":              fixtureSessionID,
		"message_id":      fixtureCheckpointID,
		"phase_id":        "approve",
		"blueprint_id":    fixtureBlueprintID,
		"scan_id":         fixtureScanID,
		"workflow_id":     "hotfix-session",
		"pack_id":         "aws-cli",
		"secret_id":       "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"entry_id":        "ignore-fixture",
		"git_change_id":   "git-change-fixture",
		"protection_id":   fixtureCheckpointID,
		"resource_id":     "res_01",
		"process_id":      "proc-1",
	}
	if strings.Contains(path, "/projects/") {
		params["id"] = fixtureProjectID
	}
	if strings.Contains(path, "/delegations/") {
		params["id"] = fixtureDelegationID
	}
	if strings.Contains(path, "/workers/") {
		params["id"] = fixtureWorkerID
	}
	if strings.Contains(path, "/workflow-runs/") && !strings.Contains(path, "/sessions/") {
		params["id"] = fixtureWorkflowRunID
	}
	return params
}

func TestOpenAPIStubStreamRouteSkippedForJSONSchema(t *testing.T) {
	t.Parallel()
	if !isIncrementalStreamRoute("/v1/sessions/{id}/stream") {
		t.Fatal("expected stream path detection")
	}
}

func TestOpenAPIRoutesHaveOperationIDs(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	routes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	for _, route := range routes {
		if route.OperationID == "" {
			t.Fatalf("%s %s missing operationId", route.Method, route.Path)
		}
	}
}

func stubURL(base, method, path string) string {
	out := path
	for name, value := range stubPathParams(path) {
		out = strings.ReplaceAll(out, "{"+name+"}", value)
	}
	url := base + out
	if method == http.MethodGet && strings.HasSuffix(out, "/workers") {
		url += "?project_id=" + fixtureProjectID
	}
	if method == http.MethodGet && strings.HasSuffix(out, "/board") {
		url += "?project_id=" + fixtureProjectID
	}
	if method == http.MethodGet && strings.HasSuffix(out, "/events") {
		url += "?project_id=" + fixtureProjectID
	}
	if method == http.MethodGet && strings.HasSuffix(out, "/cost/summary") {
		url += "?session_id=" + fixtureSessionID
	}
	if method == http.MethodGet && strings.HasSuffix(out, "/workflow-runs") {
		url += "?limit=20"
	}
	return url
}
