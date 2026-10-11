package sourcecontracts

import (
	"encoding/json"
	"net/http"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestMarkedSecretScreenTracksProjectLifecycleWithoutAChat(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	if got := f.AwaitScreen(t); len(got.Spans) != 0 {
		t.Fatalf("unmarked spans = %v", got.Spans)
	}
	start, end := f.TokenRange()
	w := f.Post(t, "mark", wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.FixtureDocument.Revision, Start: start, End: end},
		Name:  "Relay token", Purpose: "Authenticates test relay", OperationID: "screen-lifecycle",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("mark status = %d", w.Code)
	}
	var secret wire.ManagedSecret
	testutil.FailErr(t, "decode marked secret", json.Unmarshal(w.Body.Bytes(), &secret))
	contractfixture.AssertProjectSecretScreen(t, f, wire.SecretSpanState("tracked"), secret.Reference)
	_, err := f.Srv.Sources.Workspace.ManagedSecrets.ReplaceValue(t.Context(), secretcap.ReplaceValueRequest{
		ProjectID: f.Project.ID, Reference: secret.Reference, Value: "replacement-relay-value-02",
	})
	testutil.FailErr(t, "rotate marked value", err)
	contractfixture.AssertProjectSecretScreen(t, f, wire.SecretSpanState("retired"), "")
	_, err = f.Srv.Sources.Workspace.ManagedSecrets.ReplaceValue(t.Context(), secretcap.ReplaceValueRequest{
		ProjectID: f.Project.ID, Reference: secret.Reference, Value: contractfixture.RelayToken,
	})
	testutil.FailErr(t, "rotate back to retained value", err)
	contractfixture.AssertProjectSecretScreen(t, f, wire.SecretSpanState("tracked"), secret.Reference)
	_, err = f.Srv.Sources.Workspace.ManagedSecrets.RevokeProject(t.Context(), f.Project.ID, secret.Reference, contractfixture.FixtureOwner(t, f))
	testutil.FailErr(t, "revoke marked value", err)
	contractfixture.AssertProjectSecretScreen(t, f, wire.SecretSpanState("retired"), "")
}

// A chat's generated secret written into a project file screens as tracked for
// people, by reference, though no chat is attached to the screen and its chat
// no longer exists. Only an explicit revoke retires it.

func TestChatSecretScreensTrackedForPeopleUntilRevoked(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	Put, err := f.Srv.Sources.Workspace.ManagedSecrets.Put(t.Context(), secretcap.PutRequest{
		ProjectID: f.Project.ID, SessionID: "root-generator", ChatSessionID: "root-generator",
		OperationID: "chat-generated", Name: "Relay token", Purpose: "Authenticates test relay",
		Scope: secretcap.ScopeChat, Origin: secretcap.OriginGenerated,
		Format: secretcap.FormatAlphanumeric, EntropyBits: 128, Value: contractfixture.RelayToken,
	})
	testutil.FailErr(t, "put chat secret", err)
	contractfixture.AssertProjectSecretScreen(t, f, wire.SecretSpanState("tracked"), Put.Metadata.Reference)
	_, err = f.Srv.Sources.Workspace.ManagedSecrets.RevokeProject(t.Context(), f.Project.ID, Put.Metadata.Reference, contractfixture.FixtureOwner(t, f))
	testutil.FailErr(t, "revoke from settings", err)
	contractfixture.AssertProjectSecretScreen(t, f, wire.SecretSpanState("retired"), "")
}

// fixtureOwner returns the person the fixture's API calls authenticate as.
