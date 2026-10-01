package secretcap

import (
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func chronologySecret(t *testing.T, service *Service) Metadata {
	t.Helper()
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "chronology",
		Name: "History key", Purpose: "records ordered observations", Value: "entered-chronology-value",
	})
	testutil.FailErr(t, "create history secret", err)
	return meta
}

func TestUseChronologyAcrossFractionalSecondPrecision(t *testing.T) {
	service, _, _ := testService(t)
	meta := chronologySecret(t, service)
	start := service.now()
	for i, offset := range []time.Duration{0, 100 * time.Millisecond} {
		service.now = func() time.Time { return start.Add(offset) }
		_, err := service.Resolve(t.Context(), map[string]any{"value": meta.Reference}, ResolveContext{
			ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
			ToolName: "command", ToolCallID: fmt.Sprintf("call-%d", i),
		})
		testutil.FailErr(t, "record timed use", err)
	}
	history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 1)
	testutil.FailErr(t, "read newest use", err)
	if len(history.Items) != 1 || history.Items[0].ToolCallID != "call-1" {
		t.Fatalf("newest use = %+v", history)
	}
	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "read use summary", err)
	want := db.FormatTime(start.Add(100 * time.Millisecond))
	if len(listed) != 1 || listed[0].LastUsedAt == nil || *listed[0].LastUsedAt != want {
		t.Fatalf("last-use summary = %+v, want %s", listed, want)
	}
}

func TestUseRetentionBreaksTimestampTiesByAdmission(t *testing.T) {
	service, _, _ := testService(t)
	meta := chronologySecret(t, service)
	secretID, err := ParseReference(meta.Reference)
	testutil.FailErr(t, "parse history reference", err)
	const total = maxUseHistory + 5
	for i := 0; i < total; i++ {
		err := service.queries.CreateManagedSecretUse(t.Context(), db.CreateManagedSecretUseParams{
			ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", total-i), SecretID: secretID,
			ToolName: "command", ToolCallID: nullable(fmt.Sprintf("call-%d", i)),
			Outcome: UseResolved, Delivery: DeliveryPending, UsedAt: db.FormatTime(service.now()),
		})
		testutil.FailErr(t, "insert equal-time use", err)
		err = service.queries.TrimManagedSecretUses(t.Context(), db.TrimManagedSecretUsesParams{SecretID: secretID, Keep: maxUseHistory})
		testutil.FailErr(t, "trim equal-time uses", err)
	}
	history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
	testutil.FailErr(t, "read retained uses", err)
	if history.Count != maxUseHistory {
		t.Fatalf("retained %d uses, want %d", history.Count, maxUseHistory)
	}
	for i, use := range history.Items {
		if want := fmt.Sprintf("call-%d", total-1-i); use.ToolCallID != want {
			t.Fatalf("history[%d] = %s, want %s", i, use.ToolCallID, want)
		}
	}
}

func TestRevealSummaryAcrossFractionalSecondPrecision(t *testing.T) {
	service, _, _ := testService(t)
	meta := chronologySecret(t, service)
	privateKey := configureRevealKey(t, service)
	start := service.now()
	for _, offset := range []time.Duration{0, 100 * time.Millisecond} {
		service.now = func() time.Time { return start.Add(offset) }
		challenge, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, meta.Reference, "main", testOwner(t, service))
		testutil.FailErr(t, "begin timed reveal", err)
		_, err = service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, meta.Reference,
			challenge.ID, testOwner(t, service), RevealAuthenticatorMacOS, signReveal(privateKey, challenge.ProofPayload, RevealAuthenticatorMacOS))
		testutil.FailErr(t, "complete timed reveal", err)
	}
	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "read reveal summary", err)
	want := db.FormatTime(start.Add(100 * time.Millisecond))
	if len(listed) != 1 || listed[0].RevealCount != 2 || listed[0].LastRevealedAt == nil || *listed[0].LastRevealedAt != want {
		t.Fatalf("last-reveal summary = %+v, want %s", listed, want)
	}
}

func TestUseHistoryRefusesVariableWidthTimestamps(t *testing.T) {
	service, _, _ := testService(t)
	meta := chronologySecret(t, service)
	secretID, err := ParseReference(meta.Reference)
	testutil.FailErr(t, "parse history reference", err)
	for _, stamp := range []string{"2026-08-31T12:00:00Z", "2026-08-31T12:00:00.1Z", "2026-08-31T12:00:00.100000000+00:00"} {
		err := service.queries.CreateManagedSecretUse(t.Context(), db.CreateManagedSecretUseParams{
			ID: "00000000-0000-4000-8000-000000000001", SecretID: secretID,
			ToolName: "command", Outcome: UseResolved, Delivery: DeliveryPending, UsedAt: stamp,
		})
		if err == nil {
			t.Fatalf("stored noncanonical timestamp %q", stamp)
		}
	}
}
