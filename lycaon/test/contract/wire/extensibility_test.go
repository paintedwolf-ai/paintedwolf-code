package contract

import (
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestContractExtensibilityStubs(t *testing.T) {
	t.Parallel()
	srv := NewStubServer()
	t.Cleanup(srv.Close)
	client := srv.Client()
	base := srv.URL

	var cost api.CostSummary
	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/cost/summary?session_id="+fixtureSessionID, nil, http.StatusOK, &cost)
	contractcheck.AssertJSONRoundTrip(t, &cost)

	var full api.FullScanResponse
	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/projects/"+fixtureProjectID+"/scans", api.FullScanRequest{
		Kind:       "full",
		ScannerIDs: []string{"lycaon-sast"},
	}, http.StatusAccepted, &full)
	contractcheck.AssertJSONRoundTrip(t, &full)

	var scan api.CodeScan

	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/projects/"+fixtureProjectID+"/scans/"+fixtureScanID, nil, http.StatusOK, &scan)

	var servers api.McpProviderListResponse
	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/mcp/providers", nil, http.StatusOK, &servers)

	var checkResp api.McpCheckResponse
	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/mcp/providers/check", nil, http.StatusOK, &checkResp)

	var providers api.ProviderListResponse
	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/providers", nil, http.StatusOK, &providers)

	var webResearch api.WebResearchProvidersResponse
	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/web-research/providers", nil, http.StatusOK, &webResearch)
	contractcheck.AssertJSONRoundTrip(t, &webResearch)
}

func TestContractBlueprintsAndBoard(t *testing.T) {
	t.Parallel()
	srv := NewStubServer()
	t.Cleanup(srv.Close)
	client := srv.Client()
	base := srv.URL

	var summaries api.BlueprintListResponse
	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/projects/"+fixtureProjectID+"/blueprints", nil, http.StatusOK, &summaries)

	var blueprint api.Blueprint
	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/projects/"+fixtureProjectID+"/blueprints", api.CreateBlueprintRequest{
		Title: "p1",
	}, http.StatusCreated, &blueprint)

	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/projects/"+fixtureProjectID+"/blueprints/"+fixtureBlueprintID, nil, http.StatusOK, &blueprint)
	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/projects/"+fixtureProjectID+"/blueprints/"+fixtureBlueprintID+"/approve", api.BlueprintApproveRequest{}, http.StatusOK, &blueprint)

	var board api.BoardView
	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/projects/"+fixtureProjectID+"/board?session_id="+fixtureSessionID, nil, http.StatusOK, &board)
	contractcheck.AssertJSONRoundTrip(t, &board)

	var projects api.ProjectListResponse
	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/projects", nil, http.StatusOK, &projects)

	var created api.Project
	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/projects", api.CreateProjectRequest{
		Roots: []api.CreateProjectRootInput{{Path: fixtureProjectDir}},
	}, http.StatusCreated, &created)
	contractcheck.AssertJSONRoundTrip(t, &created)
	if created.ID != fixtureProjectID {
		t.Fatalf("project.id = %q, want %q", created.ID, fixtureProjectID)
	}
}
