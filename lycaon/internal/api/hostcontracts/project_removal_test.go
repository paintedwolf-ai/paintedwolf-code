package hostcontracts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProjectRemovalAssessmentAndRecordedOutcome(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	database := testdbfixture.Open(t, "removals.db")
	reg := project.NewSQLRegistry(database)
	p, err := reg.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: t.TempDir()}}})
	testutil.FailErr(t, "create project", err)
	deps := hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: sessionstore.NewSQL(database), Projects: reg}, Storage: hostapi.StorageDependencies{DataDir: t.TempDir()}}
	contractfixture.WithExtensionOwner(t)(&deps)
	server := hostapi.NewServer(contractfixture.RequiredTestDeps(t, deps), nil, hostapi.TestAPIToken)
	owner := server.Admin.Extensions.Mutations.Owner
	pack := t.TempDir()
	extpackstest.WriteMinimalPack(t, pack, "acme/removable", 1)
	extstatetest.Apply(t, owner, extstatetest.DeviceScope(), extensionstate.InstallOp{Source: "path:" + pack})
	extstatetest.Apply(t, owner, extstatetest.DeviceScope(), extensionstate.SetInstalledFromOp{PackID: "acme/removable", ProjectID: p.ID})
	path := "/v1/projects/" + p.ID
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path+"/removal-assessment", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous assessment status = %d", unauthorized.Code)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodGet, path+"/removal-assessment", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("assessment = %d %s", response.Code, response.Body.String())
	}
	var assessment wire.ProjectRemovalAssessment
	testutil.FailErr(t, "decode assessment", json.Unmarshal(response.Body.Bytes(), &assessment))
	if !assessment.Complete || len(assessment.Extensions) != 1 || assessment.Extensions[0].Disposition != "eligible" {
		t.Fatalf("assessment = %+v", assessment)
	}
	request := wire.ProjectRemovalRequest{OperationID: uuid.NewString(), AssessmentToken: assessment.AssessmentToken, RemoveExtensions: []string{"acme/removable"}}
	body, err := json.Marshal(request)
	testutil.FailErr(t, "encode request", err)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodPost, path+"/removals", bytes.NewReader(body)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("removal = %d %s", response.Code, response.Body.String())
	}
	var result wire.ProjectRemovalResult
	testutil.FailErr(t, "decode outcome", json.Unmarshal(response.Body.Bytes(), &result))
	if result.ProjectState != "deleted" || result.CleanupState != "removed" || result.Assessment == nil {
		t.Fatalf("result = %+v", result)
	}
	replay := httptest.NewRecorder()
	server.ServeHTTP(replay, contractfixture.NewAuthedRequest(http.MethodGet, path+"/removals/"+request.OperationID, nil))
	if replay.Code != http.StatusOK || replay.Body.String() != response.Body.String() {
		t.Fatalf("receipt = %d %s", replay.Code, replay.Body.String())
	}
	retry := httptest.NewRecorder()
	server.ServeHTTP(retry, contractfixture.NewAuthedRequest(http.MethodPost, path+"/removals", bytes.NewReader(body)))
	if retry.Code != http.StatusAccepted || retry.Body.String() != response.Body.String() {
		t.Fatalf("retry = %d %s", retry.Code, retry.Body.String())
	}
}
