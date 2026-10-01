package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestMarkedSecretScreenTracksProjectLifecycleWithoutAChat(t *testing.T) {
	f := newSecretSpanFixture(t)
	if got := f.awaitScreen(t); len(got.Spans) != 0 {
		t.Fatalf("unmarked spans = %v", got.Spans)
	}
	start, end := f.tokenRange()
	w := f.post(t, "mark", wire.SecretMarkRequest{
		Range: wire.SecretMarkRange{Revision: f.document.Revision, Start: start, End: end},
		Name:  "Relay token", Purpose: "Authenticates test relay", OperationID: "screen-lifecycle",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("mark status = %d", w.Code)
	}
	var secret wire.ManagedSecret
	testutil.FailErr(t, "decode marked secret", json.Unmarshal(w.Body.Bytes(), &secret))
	assertProjectSecretScreen(t, f, wire.SecretSpanState("tracked"), secret.Reference)
	_, err := f.srv.Sources.ManagedSecrets.ReplaceValue(t.Context(), secretcap.ReplaceValueRequest{
		ProjectID: f.project.ID, Reference: secret.Reference, Value: "replacement-relay-value-02",
	})
	testutil.FailErr(t, "rotate marked value", err)
	assertProjectSecretScreen(t, f, wire.SecretSpanState("retired"), "")
	_, err = f.srv.Sources.ManagedSecrets.ReplaceValue(t.Context(), secretcap.ReplaceValueRequest{
		ProjectID: f.project.ID, Reference: secret.Reference, Value: relayToken,
	})
	testutil.FailErr(t, "rotate back to retained value", err)
	assertProjectSecretScreen(t, f, wire.SecretSpanState("tracked"), secret.Reference)
	_, err = f.srv.Sources.ManagedSecrets.RevokeProject(t.Context(), f.project.ID, secret.Reference, fixtureOwner(t, f))
	testutil.FailErr(t, "revoke marked value", err)
	assertProjectSecretScreen(t, f, wire.SecretSpanState("retired"), "")
}

// A chat's generated secret written into a project file screens as tracked for
// people, by reference, though no chat is attached to the screen and its chat
// no longer exists. Only an explicit revoke retires it.
func TestChatSecretScreensTrackedForPeopleUntilRevoked(t *testing.T) {
	f := newSecretSpanFixture(t)
	put, err := f.srv.Sources.ManagedSecrets.Put(t.Context(), secretcap.PutRequest{
		ProjectID: f.project.ID, SessionID: "root-generator", ChatSessionID: "root-generator",
		OperationID: "chat-generated", Name: "Relay token", Purpose: "Authenticates test relay",
		Scope: secretcap.ScopeChat, Origin: secretcap.OriginGenerated,
		Format: secretcap.FormatAlphanumeric, EntropyBits: 128, Value: relayToken,
	})
	testutil.FailErr(t, "put chat secret", err)
	assertProjectSecretScreen(t, f, wire.SecretSpanState("tracked"), put.Metadata.Reference)
	_, err = f.srv.Sources.ManagedSecrets.RevokeProject(t.Context(), f.project.ID, put.Metadata.Reference, fixtureOwner(t, f))
	testutil.FailErr(t, "revoke from settings", err)
	assertProjectSecretScreen(t, f, wire.SecretSpanState("retired"), "")
}

// fixtureOwner returns the person the fixture's API calls authenticate as.
func fixtureOwner(t *testing.T, f secretSpanFixture) string {
	t.Helper()
	owner, err := f.srv.sessionStore.HostOwner(t.Context())
	testutil.FailErr(t, "read host owner", err)
	return owner.ID
}

func assertProjectSecretScreen(t *testing.T, f secretSpanFixture, state wire.SecretSpanState, reference string) {
	t.Helper()
	screen := f.awaitScreen(t)
	assert := func(label string, got *wire.SecretScreen) {
		t.Helper()
		if got == nil || len(got.Spans) != 1 {
			t.Fatalf("%s screen = %+v", label, got)
		}
		span := got.Spans[0]
		if span.State != state || span.Reference != reference {
			t.Fatalf("%s state/reference = %q/%q", label, span.State, span.Reference)
		}
		raw, err := json.Marshal(got)
		testutil.FailErr(t, "encode projected screen", err)
		if strings.Contains(string(raw), relayToken) {
			t.Fatalf("%s screen exposed value", label)
		}
	}
	assert("editor", screen)
	side := sourceledger.ComparisonSide{Content: fileWithSecret, Availability: sourceledger.ContentAvailable}
	comparison := sourceledger.Comparison{InRange: true, Before: side, After: side}
	got := f.srv.Sources.MapSourceComparison(t.Context(), f.project.ID, comparison)
	assert("history before", got.Before.SecretScreen)
	assert("history after", got.After.SecretScreen)
	other := f.srv.Sources.MapSourceComparison(t.Context(), uuid.NewString(), comparison)
	if other.Before.SecretScreen == nil || len(other.Before.SecretScreen.Spans) != 0 {
		t.Fatal("project evidence crossed project boundary")
	}
}
