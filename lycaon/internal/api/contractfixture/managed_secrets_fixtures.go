package contractfixture

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretcap"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func CallSecrets(t *testing.T, srv *hostapi.Server, method, target string, body any, wantStatus int) string {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		testutil.FailErr(t, "encode request", err)
		reader = strings.NewReader(string(encoded))
	}
	rec := httptest.NewRecorder()
	if reader == nil {
		srv.ServeHTTP(rec, NewAuthedRequest(method, target, nil))
	} else {
		srv.ServeHTTP(rec, NewAuthedRequest(method, target, reader))
	}
	if rec.Code != wantStatus {
		t.Fatalf("%s %s status = %d want %d body=%s", method, target, rec.Code, wantStatus, rec.Body.String())
	}
	out := rec.Body.String()
	for _, secret := range []string{EnteredValue, "sk-entered-lifecycle-000002"} {
		if strings.Contains(out, secret) {
			t.Fatalf("%s %s response exposed a protected value: %s", method, target, out)
		}
	}
	return out
}

func DecodeSecret(t *testing.T, body string) wire.ManagedSecret {
	t.Helper()
	var out wire.ManagedSecret
	testutil.FailErr(t, "decode managed secret", json.Unmarshal([]byte(body), &out))
	return out
}

func DecodeSecretList(t *testing.T, body string) wire.ManagedSecretList {
	t.Helper()
	var out wire.ManagedSecretList
	testutil.FailErr(t, "decode managed secret list", json.Unmarshal([]byte(body), &out))
	return out
}

const EnteredValue = "sk-entered-lifecycle-000001"

func NewManagedSecretServer(t *testing.T) (*hostapi.Server, *secretcap.Service, *credentialstore.Store) {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "root-chat", testdbseed.DefaultProjectID)
	values := credentialstore.NewEmpty(credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets,
		Context:   "test managed secret",
	},
		func(id string) bool { _, err := uuid.Parse(id); return err == nil },
	)
	secrets := secretcap.NewWithStore(database, values, nil)
	srv := hostapi.NewServer(RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: sessionstore.NewSQL(database), Projects: project.NewSQLRegistry(database)}, Approvals: hostapi.ApprovalsDependencies{ManagedSecrets: secrets}}), nil, hostapi.TestAPIToken)
	return srv, secrets, values
}

// callSecrets rejects responses containing a protected fixture value.

func ReferenceID(t *testing.T, reference string) string {
	t.Helper()
	id, err := secretcap.ParseReference(reference)
	testutil.FailErr(t, "parse reference", err)
	return id
}
