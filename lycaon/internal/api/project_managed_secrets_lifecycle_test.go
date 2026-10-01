package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretcap"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const enteredValue = "sk-entered-lifecycle-000001"

func TestProjectManagedSecretAdministrationRunsTheWholeLifeWithoutRevealing(t *testing.T) {
	srv, secrets, values := newManagedSecretServer(t)
	base := "/v1/projects/" + testdbseed.DefaultProjectID + "/secrets"

	created := decodeSecret(t, callSecrets(t, srv, http.MethodPost, base, map[string]any{
		"operation_id": uuid.NewString(),
		"name":         "Registry token",
		"purpose":      "publishes packages from any later chat",
		"secret_value": enteredValue,
	}, http.StatusCreated))
	if created.Origin != "settings_entered" || created.Scope != "project" || created.Version != 1 {
		t.Fatalf("created capability = %+v", created)
	}
	id := referenceID(t, created.Reference)

	updated := decodeSecret(t, callSecrets(t, srv, http.MethodPatch, base+"/"+id, map[string]any{
		"name":              "Package registry token",
		"agent_use_ends_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}, http.StatusOK))
	if updated.Name != "Package registry token" || updated.AgentUseEndsAt == nil {
		t.Fatalf("updated capability = %+v", updated)
	}

	replaced := decodeSecret(t, callSecrets(t, srv, http.MethodPut, base+"/"+id+"/value", map[string]any{
		"secret_value": "sk-entered-lifecycle-000002",
	}, http.StatusOK))
	if replaced.Reference != created.Reference || replaced.Version != 2 || replaced.ValueReplacedAt == nil {
		t.Fatalf("replaced capability = %+v", replaced)
	}

	if ids := values.IDs(); len(ids) != 2 {
		t.Fatalf("stored versions = %v", ids)
	}

	_, err := secrets.Resolve(t.Context(), map[string]any{"value": created.Reference}, secretcap.ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-chat",
		SessionID: "root-chat", ToolName: "http_request",
	})
	testutil.FailErr(t, "resolve replaced reference", err)

	usesBody := callSecrets(t, srv, http.MethodGet, base+"/"+id+"/uses", nil, http.StatusOK)
	var uses wire.ManagedSecretUseList
	testutil.FailErr(t, "decode uses", json.Unmarshal([]byte(usesBody), &uses))
	if len(uses.Uses) != 1 || uses.Uses[0].Outcome != "resolved" || uses.Uses[0].ToolName != "http_request" {
		t.Fatalf("use history = %+v", uses)
	}

	callSecrets(t, srv, http.MethodDelete, base+"/"+id, nil, http.StatusNoContent)
	listedRevoked := decodeSecretList(t, callSecrets(t, srv, http.MethodGet, base, nil, http.StatusOK))
	if len(listedRevoked.Secrets) != 1 || listedRevoked.Secrets[0].State != "revoked" {
		t.Fatalf("revoked state = %v", listedRevoked.Secrets)
	}
	callSecrets(t, srv, http.MethodPatch, base+"/"+id, map[string]any{"name": "after"}, http.StatusConflict)
	callSecrets(t, srv, http.MethodPut, base+"/"+id+"/value",
		map[string]any{"secret_value": "sk-entered-lifecycle-000003"}, http.StatusConflict)
}

func TestProjectManagedSecretCreateRefusesAValueWithNoPurpose(t *testing.T) {
	srv, _, _ := newManagedSecretServer(t)
	body := callSecrets(t, srv, http.MethodPost,
		"/v1/projects/"+testdbseed.DefaultProjectID+"/secrets", map[string]any{
			"name":         "Unlabelled",
			"purpose":      "",
			"secret_value": enteredValue,
		}, http.StatusBadRequest)
	if strings.Contains(body, enteredValue) {
		t.Fatal("refusal echoed the value")
	}
}

func TestProjectManagedSecretAdministrationRefusesAnUnknownCapability(t *testing.T) {
	srv, _, _ := newManagedSecretServer(t)
	base := "/v1/projects/" + testdbseed.DefaultProjectID + "/secrets/"
	callSecrets(t, srv, http.MethodPatch, base+uuid.NewString(),
		map[string]any{"name": "ghost"}, http.StatusNotFound)
	callSecrets(t, srv, http.MethodGet, base+uuid.NewString()+"/uses", nil, http.StatusNotFound)
	callSecrets(t, srv, http.MethodPatch, base+"not-a-uuid",
		map[string]any{"name": "ghost"}, http.StatusBadRequest)
}

func TestProjectManagedSecretRevealRequiresNativeProofAndAuditsSuccess(t *testing.T) {
	srv, secrets, _ := newManagedSecretServer(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate native reveal key", err)
	testutil.FailErr(t, "configure native reveal key", secrets.ConfigureRevealPublicKey(
		base64.RawURLEncoding.EncodeToString(publicKey),
	))
	base := "/v1/projects/" + testdbseed.DefaultProjectID + "/secrets"
	created := decodeSecret(t, callSecrets(t, srv, http.MethodPost, base, map[string]any{
		"operation_id": uuid.NewString(),
		"name":         "Revealable key", "purpose": "exercise native authentication", "secret_value": enteredValue,
	}, http.StatusCreated))
	id := referenceID(t, created.Reference)

	begin := func() wire.ManagedSecretRevealChallenge {
		body := callSecrets(t, srv, http.MethodPost, base+"/"+id+"/reveal-challenges",
			map[string]any{"window_label": "main"}, http.StatusCreated)
		var challenge wire.ManagedSecretRevealChallenge
		testutil.FailErr(t, "decode reveal challenge", json.Unmarshal([]byte(body), &challenge))
		return challenge
	}
	bad := begin()
	callSecrets(t, srv, http.MethodPost,
		base+"/"+id+"/reveal-challenges/"+bad.ChallengeID+"/complete",
		map[string]any{"authenticator": secretcap.RevealAuthenticatorMacOS, "signature": "invalid"},
		http.StatusForbidden)

	challenge := begin()
	message := []byte("painted-wolf-managed-secret-reveal-v1\n" + challenge.ProofPayload +
		"\nauthenticator=" + secretcap.RevealAuthenticatorMacOS)
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	encoded, err := json.Marshal(wire.CompleteManagedSecretRevealRequest{
		Authenticator: secretcap.RevealAuthenticatorMacOS, Signature: signature,
	})
	testutil.FailErr(t, "encode reveal proof", err)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, newAuthedRequest(http.MethodPost,
		base+"/"+id+"/reveal-challenges/"+challenge.ChallengeID+"/complete", strings.NewReader(string(encoded))))
	if rec.Code != http.StatusOK {
		t.Fatalf("complete reveal status = %d body=%s", rec.Code, rec.Body.String())
	}
	var revealed wire.ManagedSecretRevealResponse
	testutil.FailErr(t, "decode reveal response", json.Unmarshal(rec.Body.Bytes(), &revealed))
	if revealed.SecretValue != enteredValue || revealed.Version != 1 || revealed.RemaskAfterMs != 30000 {
		t.Fatalf("reveal response = %+v", revealed)
	}

	listed := decodeSecretList(t, callSecrets(t, srv, http.MethodGet, base, nil, http.StatusOK))
	if len(listed.Secrets) == 0 || listed.Secrets[0].RevealCount != 1 || listed.Secrets[0].LastRevealedAt == nil {
		t.Fatalf("reveal audit metadata = %+v", listed.Secrets)
	}
}

func newManagedSecretServer(t *testing.T) (*Server, *secretcap.Service, *credentialstore.Store) {
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
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: sessionstore.NewSQL(database), Projects: project.NewSQLRegistry(database), ManagedSecrets: secrets,
	}), nil, TestAPIToken)
	return srv, secrets, values
}

// callSecrets rejects responses containing a protected fixture value.
func callSecrets(t *testing.T, srv *Server, method, target string, body any, wantStatus int) string {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		testutil.FailErr(t, "encode request", err)
		reader = strings.NewReader(string(encoded))
	}
	rec := httptest.NewRecorder()
	if reader == nil {
		srv.ServeHTTP(rec, newAuthedRequest(method, target, nil))
	} else {
		srv.ServeHTTP(rec, newAuthedRequest(method, target, reader))
	}
	if rec.Code != wantStatus {
		t.Fatalf("%s %s status = %d want %d body=%s", method, target, rec.Code, wantStatus, rec.Body.String())
	}
	out := rec.Body.String()
	for _, secret := range []string{enteredValue, "sk-entered-lifecycle-000002"} {
		if strings.Contains(out, secret) {
			t.Fatalf("%s %s response exposed a protected value: %s", method, target, out)
		}
	}
	return out
}

func decodeSecret(t *testing.T, body string) wire.ManagedSecret {
	t.Helper()
	var out wire.ManagedSecret
	testutil.FailErr(t, "decode managed secret", json.Unmarshal([]byte(body), &out))
	return out
}

func decodeSecretList(t *testing.T, body string) wire.ManagedSecretList {
	t.Helper()
	var out wire.ManagedSecretList
	testutil.FailErr(t, "decode managed secret list", json.Unmarshal([]byte(body), &out))
	return out
}

func referenceID(t *testing.T, reference string) string {
	t.Helper()
	id, err := secretcap.ParseReference(reference)
	testutil.FailErr(t, "parse reference", err)
	return id
}
