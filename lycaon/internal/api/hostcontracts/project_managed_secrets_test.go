package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretcap"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProjectManagedSecretsAPIListsMetadataAndRevokesWithoutRevealing(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")

	rootTaskID := uuid.NewString()
	testdbseed.InsertSession(t, database, rootTaskID, testdbseed.DefaultProjectID)
	values := credentialstore.NewEmpty(credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets,
		Context:   "test managed secret",
	},
		func(id string) bool { _, err := uuid.Parse(id); return err == nil },
	)
	secrets := secretcap.NewWithStore(database, values, nil)
	meta, err := secrets.Generate(t.Context(), secretcap.GenerateRequest{
		ProjectID:   testdbseed.DefaultProjectID,
		SessionID:   rootTaskID,
		OperationID: "generate-api-test",
		Name:        "Webhook signing key",
		Purpose:     "Authenticate callbacks",
		Scope:       secretcap.ScopeProject,
	})
	testutil.FailErr(t, "generate secret", err)
	id, err := secretcap.ParseReference(meta.Reference)
	testutil.FailErr(t, "parse reference", err)
	// Stored values are keyed by version.
	version, err := db.New(database).GetCurrentManagedSecretVersion(t.Context(), id)
	testutil.FailErr(t, "read current version", err)
	value, ok := values.Get(version.ID)
	if !ok {
		t.Fatal("generated value missing from protected store")
	}

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store:    sessionstore.NewSQL(database),
		Projects: project.NewSQLRegistry(database)}, Approvals: hostapi.ApprovalsDependencies{
		ManagedSecrets: secrets}}), nil, hostapi.TestAPIToken)

	list := httptest.NewRecorder()
	srv.ServeHTTP(list, contractfixture.NewAuthedRequest(
		http.MethodGet,
		"/v1/projects/"+testdbseed.DefaultProjectID+"/secrets",
		nil,
	))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), value.Value()) {
		t.Fatal("metadata response exposed protected value")
	}
	var inventory wire.ManagedSecretList
	testutil.FailErr(t, "decode inventory", json.Unmarshal(list.Body.Bytes(), &inventory))
	if len(inventory.Secrets) != 1 || inventory.Secrets[0].Reference != meta.Reference {
		t.Fatalf("inventory = %#v", inventory)
	}

	revoke := httptest.NewRecorder()
	srv.ServeHTTP(revoke, contractfixture.NewAuthedRequest(
		http.MethodDelete,
		"/v1/projects/"+testdbseed.DefaultProjectID+"/secrets/"+id,
		nil,
	))
	if revoke.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d body=%s", revoke.Code, revoke.Body.String())
	}
	listAfterRevoke := httptest.NewRecorder()
	srv.ServeHTTP(listAfterRevoke, contractfixture.NewAuthedRequest(
		http.MethodGet,
		"/v1/projects/"+testdbseed.DefaultProjectID+"/secrets",
		nil,
	))
	if listAfterRevoke.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listAfterRevoke.Code, listAfterRevoke.Body.String())
	}
	var afterRevoke wire.ManagedSecretList
	testutil.FailErr(t, "decode inventory after revoke", json.Unmarshal(listAfterRevoke.Body.Bytes(), &afterRevoke))
	if len(afterRevoke.Secrets) != 1 || afterRevoke.Secrets[0].State != "revoked" {
		t.Fatalf("after revoke state = %v", afterRevoke.Secrets)
	}
	if _, ok := values.Get(version.ID); !ok {
		t.Fatal("revocation removed provider-screening evidence")
	}
}
