package secretcap

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDurableEvidenceSurvivesLifecycleWithoutAChat(t *testing.T) {
	service, values, _ := testService(t)
	ctx := t.Context()
	for _, origin := range []string{OriginSettingsEntered, OriginFileMarked, OriginComposerMarked, OriginGenerated, OriginDetected, OriginAskUserResponse} {
		t.Run(origin, func(t *testing.T) {
			before, after := "orchard-"+origin+"-before", "orchard-"+origin+"-after"
			req := PutRequest{ProjectID: testdbseed.DefaultProjectID, OperationID: origin, Name: origin, Purpose: "durable screening", Scope: ScopeProject, Origin: origin, PersonID: putPerson(t, service, origin), Value: before}
			if origin == OriginGenerated {
				req.Format, req.EntropyBits = FormatHex, 128
			}
			if !sessionlessOrigin(origin) {
				req.SessionID, req.ChatSessionID = "root-1", "root-1"
			}
			put, err := service.Put(ctx, req)
			testutil.FailErr(t, "put protected value", err)
			assertDurableValues(t, service, before)
			_, err = service.ReplaceValue(ctx, ReplaceValueRequest{ProjectID: req.ProjectID, Reference: put.Metadata.Reference, Value: after})
			testutil.FailErr(t, "replace protected value", err)
			_, err = service.RevokeProject(ctx, req.ProjectID, put.Metadata.Reference, testOwner(t, service))
			testutil.FailErr(t, "revoke protected value", err)
			assertDurableValues(t, service, before, after)
		})
	}
	restarted := NewWithStore(service.handle, values, nil)
	testutil.FailErr(t, "restore protected evidence", restarted.Reconcile(ctx))
	assertDurableValues(t, restarted, "orchard-file_marked-before", "orchard-file_marked-after")
	if len(restarted.DurableScreeningValues("other-project")) != 0 {
		t.Fatal("project-attributed screening included another project's values")
	}
	if len(restarted.DurableScreeningValues("")) == 0 {
		t.Fatal("unattributed diagnostics lost retained values")
	}
}

func assertDurableValues(t *testing.T, service *Service, expected ...string) {
	t.Helper()
	found := make(map[string]bool)
	for _, value := range service.DurableScreeningValues(testdbseed.DefaultProjectID) {
		if value.Reference != "" || !value.NonDisclosable {
			t.Fatal("durable evidence acquired substitution authority")
		}
		found[value.Secret] = true
	}
	for _, value := range expected {
		if !found[value] {
			t.Errorf("missing protected lifecycle value %q", value)
		}
	}
}

// Standing is the ledger's fact about the bytes, so it follows every explicit
// retirement and survives a restart that rebuilds the projection. Deleting a
// chat retires nothing.
func TestDurableEvidenceFollowsTheLedgerStanding(t *testing.T) {
	service, values, _ := testService(t)
	ctx := t.Context()
	replaced := putChatSecret(t, service, "replaced", "root-1", "standing-replaced-before")
	_, err := service.ReplaceValue(ctx, ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: replaced, Value: "standing-replaced-after",
	})
	testutil.FailErr(t, "replace value", err)
	byPerson := putChatSecret(t, service, "revoked-by-person", "root-1", "standing-revoked-by-person")
	_, err = service.RevokeProject(ctx, testdbseed.DefaultProjectID, byPerson, testOwner(t, service))
	testutil.FailErr(t, "revoke from settings", err)
	byAgent := putChatSecret(t, service, "revoked-by-agent", "root-1", "standing-revoked-by-agent")
	_, err = service.RevokeByAgent(ctx, testdbseed.DefaultProjectID, "root-1", byAgent)
	testutil.FailErr(t, "revoke from the chat", err)
	putChatSecret(t, service, "deleted-chat", "root-2", "standing-deleted-chat")
	deleteChat(t, service, "root-2")

	retired := map[string]bool{
		"standing-replaced-before": true, "standing-replaced-after": false,
		"standing-revoked-by-person": true, "standing-revoked-by-agent": true,
		"standing-deleted-chat": false,
	}
	assertDurableStanding(t, service, retired)
	restarted := NewWithStore(service.handle, values, nil)
	testutil.FailErr(t, "restore protected evidence", restarted.Reconcile(ctx))
	assertDurableStanding(t, restarted, retired)
}

func putChatSecret(t *testing.T, service *Service, name, chatSessionID, value string) string {
	t.Helper()
	put, err := service.Put(t.Context(), PutRequest{
		ProjectID: testdbseed.DefaultProjectID, SessionID: chatSessionID, ChatSessionID: chatSessionID,
		OperationID: name, Name: name, Purpose: "screening standing", Scope: ScopeChat,
		Origin: OriginGenerated, Format: FormatHex, EntropyBits: 128, Value: value,
	})
	testutil.FailErr(t, "put chat secret", err)
	return put.Metadata.Reference
}

func assertDurableStanding(t *testing.T, service *Service, retired map[string]bool) {
	t.Helper()
	got := make(map[string]bool)
	for _, value := range service.DurableScreeningValues(testdbseed.DefaultProjectID) {
		got[value.Secret] = value.Retired
	}
	for value, want := range retired {
		standing, ok := got[value]
		if !ok {
			t.Errorf("missing protected value %q", value)
			continue
		}
		if standing != want {
			t.Errorf("%q retired = %v, want %v", value, standing, want)
		}
	}
}

func TestDurableEvidenceDoesNotWaitOnTheMetadataWriter(t *testing.T) {
	service, _, _ := testService(t)
	// A blocked secret mutation holds both locks; screening reads its memory snapshot.
	tx, err := service.handle.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "hold transcript transaction", err)
	defer func() { _ = tx.Rollback() }()
	service.mutationMu.Lock()
	defer service.mutationMu.Unlock()
	owner := secretIdentity{id: "secret", projectID: testdbseed.DefaultProjectID, name: "token", origin: OriginFileMarked}
	service.protectDurableVersion("version", owner, "orchard-held-writer")
	done := make(chan struct{})
	go func() { service.DurableScreeningValues(testdbseed.DefaultProjectID); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("durable screening waited on metadata mutation")
	}
}

func TestDurableCookieEvidenceScreensValuesAcrossRotationAndRestart(t *testing.T) {
	service, values, _ := testService(t)
	site, err := url.Parse("https://example.test/")
	testutil.FailErr(t, "parse cookie site", err)
	for i, value := range []string{"orchard-cookie-before", "orchard-cookie-after"} {
		req := jarRequest(value)
		jar, err := service.OpenCookieJar(t.Context(), req)
		testutil.FailErr(t, "open cookie jar", err)
		jar.Store.SetCookies(site, []*http.Cookie{{Name: "session", Value: value, Path: "/"}})
		_, err = service.SaveCookieJar(t.Context(), req, jar)
		testutil.FailErr(t, "save cookie jar", err)
		if i == 1 {
			assertDurableValues(t, service, "orchard-cookie-before", value)
		}
	}
	restarted := NewWithStore(service.handle, values, nil)
	testutil.FailErr(t, "restore cookie evidence", restarted.Reconcile(context.Background()))
	assertDurableValues(t, restarted, "orchard-cookie-before", "orchard-cookie-after")
	for _, value := range restarted.DurableScreeningValues("") {
		if value.Name != "cookie" {
			t.Fatal("cookie jar document was mistaken for a credential")
		}
	}
}
