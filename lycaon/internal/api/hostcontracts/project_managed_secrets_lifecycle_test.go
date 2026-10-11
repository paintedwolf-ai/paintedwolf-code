package hostcontracts

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProjectManagedSecretAdministrationRunsTheWholeLifeWithoutRevealing(t *testing.T) {
	srv, secrets, values := contractfixture.NewManagedSecretServer(t)
	base := "/v1/projects/" + testdbseed.DefaultProjectID + "/secrets"

	created := contractfixture.DecodeSecret(t, contractfixture.CallSecrets(t, srv, http.MethodPost, base, map[string]any{
		"operation_id": uuid.NewString(),
		"name":         "Registry token",
		"purpose":      "publishes packages from any later chat",
		"secret_value": contractfixture.EnteredValue,
	}, http.StatusCreated))
	if created.Origin != "settings_entered" || created.Scope != "project" || created.Version != 1 {
		t.Fatalf("created capability = %+v", created)
	}
	id := contractfixture.ReferenceID(t, created.Reference)

	updated := contractfixture.DecodeSecret(t, contractfixture.CallSecrets(t, srv, http.MethodPatch, base+"/"+id, map[string]any{
		"name":              "Package registry token",
		"agent_use_ends_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}, http.StatusOK))
	if updated.Name != "Package registry token" || updated.AgentUseEndsAt == nil {
		t.Fatalf("updated capability = %+v", updated)
	}

	replaced := contractfixture.DecodeSecret(t, contractfixture.CallSecrets(t, srv, http.MethodPut, base+"/"+id+"/value", map[string]any{
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

	usesBody := contractfixture.CallSecrets(t, srv, http.MethodGet, base+"/"+id+"/uses", nil, http.StatusOK)
	var uses wire.ManagedSecretUseList
	testutil.FailErr(t, "decode uses", json.Unmarshal([]byte(usesBody), &uses))
	if len(uses.Uses) != 1 || uses.Uses[0].Outcome != "resolved" || uses.Uses[0].ToolName != "http_request" {
		t.Fatalf("use history = %+v", uses)
	}

	contractfixture.CallSecrets(t, srv, http.MethodDelete, base+"/"+id, nil, http.StatusNoContent)
	listedRevoked := contractfixture.DecodeSecretList(t, contractfixture.CallSecrets(t, srv, http.MethodGet, base, nil, http.StatusOK))
	if len(listedRevoked.Secrets) != 1 || listedRevoked.Secrets[0].State != "revoked" {
		t.Fatalf("revoked state = %v", listedRevoked.Secrets)
	}
	contractfixture.CallSecrets(t, srv, http.MethodPatch, base+"/"+id, map[string]any{"name": "after"}, http.StatusConflict)
	contractfixture.CallSecrets(t, srv, http.MethodPut, base+"/"+id+"/value",
		map[string]any{"secret_value": "sk-entered-lifecycle-000003"}, http.StatusConflict)
}

func TestProjectManagedSecretCreateRefusesAValueWithNoPurpose(t *testing.T) {
	srv, _, _ := contractfixture.NewManagedSecretServer(t)
	body := contractfixture.CallSecrets(t, srv, http.MethodPost,
		"/v1/projects/"+testdbseed.DefaultProjectID+"/secrets", map[string]any{
			"name":         "Unlabelled",
			"purpose":      "",
			"secret_value": contractfixture.EnteredValue,
		}, http.StatusBadRequest)
	if strings.Contains(body, contractfixture.EnteredValue) {
		t.Fatal("refusal echoed the value")
	}
}

func TestProjectManagedSecretAdministrationRefusesAnUnknownCapability(t *testing.T) {
	srv, _, _ := contractfixture.NewManagedSecretServer(t)
	base := "/v1/projects/" + testdbseed.DefaultProjectID + "/secrets/"
	contractfixture.CallSecrets(t, srv, http.MethodPatch, base+uuid.NewString(),
		map[string]any{"name": "ghost"}, http.StatusNotFound)
	contractfixture.CallSecrets(t, srv, http.MethodGet, base+uuid.NewString()+"/uses", nil, http.StatusNotFound)
	contractfixture.CallSecrets(t, srv, http.MethodPatch, base+"not-a-uuid",
		map[string]any{"name": "ghost"}, http.StatusBadRequest)
}

func TestProjectManagedSecretRevealRequiresNativeProofAndAuditsSuccess(t *testing.T) {
	srv, secrets, _ := contractfixture.NewManagedSecretServer(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate native reveal key", err)
	broker := presence.NewBroker()
	testutil.FailErr(t, "configure native reveal key", broker.Configure(base64.RawURLEncoding.EncodeToString(publicKey)))
	secrets.SetPresence(broker)
	base := "/v1/projects/" + testdbseed.DefaultProjectID + "/secrets"
	created := contractfixture.DecodeSecret(t, contractfixture.CallSecrets(t, srv, http.MethodPost, base, map[string]any{
		"operation_id": uuid.NewString(),
		"name":         "Revealable key", "purpose": "exercise native authentication", "secret_value": contractfixture.EnteredValue,
	}, http.StatusCreated))
	id := contractfixture.ReferenceID(t, created.Reference)

	begin := func() wire.ManagedSecretRevealChallenge {
		body := contractfixture.CallSecrets(t, srv, http.MethodPost, base+"/"+id+"/reveal-challenges",
			map[string]any{"window_label": "main"}, http.StatusCreated)
		var challenge wire.ManagedSecretRevealChallenge
		testutil.FailErr(t, "decode reveal challenge", json.Unmarshal([]byte(body), &challenge))
		return challenge
	}
	bad := begin()
	contractfixture.CallSecrets(t, srv, http.MethodPost,
		base+"/"+id+"/reveal-challenges/"+bad.ChallengeID+"/complete",
		map[string]any{"authenticator": presence.AuthenticatorMacOS, "signature": "invalid"},
		http.StatusForbidden)

	challenge := begin()
	message := presence.SigningMessage(challenge.ProofPayload, presence.AuthenticatorMacOS)
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	encoded, err := json.Marshal(wire.CompleteManagedSecretRevealRequest{
		Authenticator: wire.PresenceAuthenticatorMacOS, Signature: signature,
	})
	testutil.FailErr(t, "encode reveal proof", err)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(http.MethodPost,
		base+"/"+id+"/reveal-challenges/"+challenge.ChallengeID+"/complete", strings.NewReader(string(encoded))))
	if rec.Code != http.StatusOK {
		t.Fatalf("complete reveal status = %d body=%s", rec.Code, rec.Body.String())
	}
	var revealed wire.ManagedSecretRevealResponse
	testutil.FailErr(t, "decode reveal response", json.Unmarshal(rec.Body.Bytes(), &revealed))
	if revealed.SecretValue != contractfixture.EnteredValue || revealed.Version != 1 || revealed.RemaskAfterMs != 30000 {
		t.Fatalf("reveal response = %+v", revealed)
	}

	listed := contractfixture.DecodeSecretList(t, contractfixture.CallSecrets(t, srv, http.MethodGet, base, nil, http.StatusOK))
	if len(listed.Secrets) == 0 || listed.Secrets[0].RevealCount != 1 || listed.Secrets[0].LastRevealedAt == nil {
		t.Fatalf("reveal audit metadata = %+v", listed.Secrets)
	}
}
