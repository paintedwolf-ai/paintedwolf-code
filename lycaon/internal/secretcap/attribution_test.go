package secretcap

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPutNamesAPersonExactlyWhenAPersonSuppliedTheValue(t *testing.T) {
	service, _, _ := testService(t)
	_, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, OperationID: "unattributed-settings",
		Name: "Unattributed", Purpose: "entered without a person", Value: "entered-value-unattributed",
	})
	if !errors.Is(err, ErrInvalidPut) {
		t.Fatalf("settings secret without a person error = %v", err)
	}
	_, err = service.Put(t.Context(), PutRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "attributed-detection", Name: "detected", Scope: ScopeChat,
		Origin: OriginDetected, PersonID: testOwner(t, service), Value: "detected-value-attributed",
	})
	if !errors.Is(err, ErrInvalidPut) {
		t.Fatalf("detected value naming a person error = %v", err)
	}
	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list project secrets", err)
	if len(listed) != 0 {
		t.Fatalf("a misattributed value was stored: %+v", listed)
	}
}

func TestSettingsSecretRecordsItsCreator(t *testing.T) {
	service, _, _ := testService(t)
	owner := testOwner(t, service)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: owner, OperationID: "attributed-settings",
		Name: "Attributed", Purpose: "entered by the owner", Value: "entered-value-attributed",
	})
	testutil.FailErr(t, "create settings secret", err)
	id, err := ParseReference(meta.Reference)
	testutil.FailErr(t, "parse reference", err)
	var creator string
	testutil.FailErr(t, "read creator", service.handle.QueryRowContext(t.Context(),
		`SELECT created_by_person_id FROM managed_secrets WHERE id = ?`, id).Scan(&creator))
	if creator != owner {
		t.Fatalf("created_by_person_id = %q, want %q", creator, owner)
	}
}

func TestRevokeProjectRecordsTheRevokingPerson(t *testing.T) {
	service, _, _ := testService(t)
	owner := testOwner(t, service)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: owner, OperationID: "attributed-revoke",
		Name: "Revoked", Purpose: "revoked by the owner", Value: "entered-value-revoked-by",
	})
	testutil.FailErr(t, "create settings secret", err)
	if _, err := service.RevokeProject(t.Context(), testdbseed.DefaultProjectID, meta.Reference, " "); err == nil {
		t.Fatal("revoke without a person succeeded")
	}
	_, err = service.RevokeProject(t.Context(), testdbseed.DefaultProjectID, meta.Reference, owner)
	testutil.FailErr(t, "revoke", err)
	id, err := ParseReference(meta.Reference)
	testutil.FailErr(t, "parse reference", err)
	var revokedBy, revoker string
	testutil.FailErr(t, "read revoker", service.handle.QueryRowContext(t.Context(),
		`SELECT revoked_by, revoked_by_person_id FROM managed_secrets WHERE id = ?`, id).Scan(&revokedBy, &revoker))
	if revokedBy != "person" || revoker != owner {
		t.Fatalf("revoked_by = %q, revoked_by_person_id = %q, want person/%q", revokedBy, revoker, owner)
	}
}

func TestRevealCompletesOnlyForThePersonWhoBeganIt(t *testing.T) {
	service, _, _ := testService(t)
	owner := testOwner(t, service)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: owner, OperationID: "attributed-reveal",
		Name: "Revealed", Purpose: "revealed to the owner", Value: "opaque-reveal-attributed",
	})
	testutil.FailErr(t, "create settings secret", err)
	privateKey := configureRevealKey(t, service)

	stolen, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, meta.Reference, "main", owner)
	testutil.FailErr(t, "begin reveal", err)
	if _, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, meta.Reference,
		stolen.ID, "00000000-0000-4000-8000-0000000000ee", RevealAuthenticatorMacOS,
		signReveal(privateKey, stolen.ProofPayload, RevealAuthenticatorMacOS)); !errors.Is(err, ErrRevealDenied) {
		t.Fatalf("reveal completed by another person error = %v", err)
	}

	challenge, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, meta.Reference, "main", owner)
	testutil.FailErr(t, "begin reveal again", err)
	_, err = service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, meta.Reference,
		challenge.ID, owner, RevealAuthenticatorMacOS,
		signReveal(privateKey, challenge.ProofPayload, RevealAuthenticatorMacOS))
	testutil.FailErr(t, "complete reveal", err)
	id, err := ParseReference(meta.Reference)
	testutil.FailErr(t, "parse reference", err)
	var count int
	var person string
	testutil.FailErr(t, "read reveal audit", service.handle.QueryRowContext(t.Context(),
		`SELECT COUNT(*), MAX(person_id) FROM managed_secret_reveals WHERE secret_id = ?`, id).Scan(&count, &person))
	if count != 1 || person != owner {
		t.Fatalf("reveal audit = %d rows by %q, want 1 by %q", count, person, owner)
	}
}
