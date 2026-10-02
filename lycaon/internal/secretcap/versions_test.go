package secretcap

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRotationKeepsTheReferenceAndResolvesToTheNewValue(t *testing.T) {
	service, values, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "replace-1",
		Name: "Registry token", Purpose: "publishes packages", Value: "entered-value-before-01",
	})
	testutil.FailErr(t, "create settings secret", err)
	before := currentValueID(t, service, meta.Reference)

	replaced, err := service.ReplaceValue(t.Context(), ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "entered-value-after-002",
	})
	testutil.FailErr(t, "replace value", err)
	if replaced.Reference != meta.Reference {
		t.Fatalf("replacement changed the reference: %q then %q", meta.Reference, replaced.Reference)
	}
	if replaced.Version != 2 || replaced.ValueReplacedAt == nil || replaced.State != StateActive {
		t.Fatalf("replaced metadata = %+v", replaced)
	}

	resolved, err := service.Resolve(t.Context(), map[string]any{"value": meta.Reference}, ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", ToolName: "command",
	})
	testutil.FailErr(t, "resolve after replacement", err)
	if resolved.Arguments["value"] != "entered-value-after-002" {
		t.Fatalf("resolved value = %v", resolved.Arguments["value"])
	}
	if entry, ok := storedEntry(values, before); !ok || entry.Value != "entered-value-before-01" {
		t.Fatal("replacement deleted the value the agent has already seen")
	}
}

func TestScreeningGenerationChangesOnlyWhenNewProtectedBytesAreStored(t *testing.T) {
	service, _, _ := testService(t)
	initial := service.ScreeningGeneration()
	request := CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "screening-generation",
		Name: "Capture token", Purpose: "refresh capture screening", Value: "capture-generation-before",
	}
	meta, err := service.CreateSettingsSecret(t.Context(), request)
	testutil.FailErr(t, "create settings secret", err)
	afterCreate := service.ScreeningGeneration()
	if afterCreate != initial+1 {
		t.Fatalf("generation after create = %d, want %d", afterCreate, initial+1)
	}
	_, err = service.CreateSettingsSecret(t.Context(), request)
	testutil.FailErr(t, "repeat idempotent create", err)
	if got := service.ScreeningGeneration(); got != afterCreate {
		t.Fatalf("generation after idempotent create = %d, want %d", got, afterCreate)
	}
	_, err = service.ReplaceValue(t.Context(), ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "capture-generation-after",
	})
	testutil.FailErr(t, "replace value", err)
	if got := service.ScreeningGeneration(); got != afterCreate+1 {
		t.Fatalf("generation after replacement = %d, want %d", got, afterCreate+1)
	}
}

func TestRetiredValueKeepsRedactingWithoutOfferingAReference(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "replace-screen",
		Name: "Replaced key", Purpose: "screening after replacement", Value: "entered-value-screen-old",
	})
	testutil.FailErr(t, "create settings secret", err)
	_, err = service.ReplaceValue(t.Context(), ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "entered-value-screen-new",
	})
	testutil.FailErr(t, "replace value", err)

	var got []secretmatch.Remembered
	service.remember = func(_ string, items []secretmatch.Remembered) { got = append(got, items...) }
	testutil.FailErr(t, "prime screening", service.RememberProjectValues(
		t.Context(), testdbseed.DefaultProjectID, "root-1",
	))

	byValue := map[string]secretmatch.Remembered{}
	for _, item := range got {
		byValue[item.Secret] = item
	}
	old, hasOld := byValue["entered-value-screen-old"]
	fresh, hasNew := byValue["entered-value-screen-new"]
	if !hasOld || !hasNew {
		t.Fatalf("screening set = %+v", got)
	}
	if old.Reference != "" {
		t.Fatalf("retired value still offers a reference: %q", old.Reference)
	}
	if fresh.Reference != meta.Reference {
		t.Fatalf("current value reference = %q", fresh.Reference)
	}
	if !old.NonDisclosable || !fresh.NonDisclosable {
		t.Fatal("replaced values are disclosable")
	}
}

func TestRestoreBringsBackAnUnavailableCapabilityUnderTheSameReference(t *testing.T) {
	service, values, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "restore-1",
		Name: "Lost key", Purpose: "value store was cleared", Value: "entered-value-lost-0001",
	})
	testutil.FailErr(t, "create settings secret", err)
	testutil.FailErr(t, "drop stored value", values.Delete(currentValueID(t, service, meta.Reference)))

	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list", err)
	if len(listed) != 1 || listed[0].State != StateUnavailable {
		t.Fatalf("state after losing the value = %+v", listed)
	}

	restored, err := service.ReplaceValue(t.Context(), ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "entered-value-restored-1",
	})
	testutil.FailErr(t, "restore", err)
	if restored.Reference != meta.Reference || restored.State != StateActive || restored.Version != 2 {
		t.Fatalf("restored metadata = %+v", restored)
	}
	resolved, err := service.Resolve(t.Context(), map[string]any{"value": meta.Reference}, ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", ToolName: "command",
	})
	testutil.FailErr(t, "resolve restored", err)
	if resolved.Arguments["value"] != "entered-value-restored-1" {
		t.Fatalf("restored resolution = %v", resolved.Arguments["value"])
	}
}

// Equal-value rotations return the same response shape as other rotations.
func TestRotationOntoTheSameBytesIsNotAnOracle(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "replace-noop",
		Name: "Unchanged", Purpose: "rotating onto itself", Value: "entered-value-same-0001",
	})
	testutil.FailErr(t, "create settings secret", err)
	same, err := service.ReplaceValue(t.Context(), ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "entered-value-same-0001",
	})
	testutil.FailErr(t, "rotate onto the same bytes", err)
	different, err := service.ReplaceValue(t.Context(), ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "entered-value-same-0002",
	})
	testutil.FailErr(t, "rotate onto different bytes", err)
	if same.Version != 2 || different.Version != 3 || same.State != different.State {
		t.Fatalf("matching and differing rotations are distinguishable: %+v vs %+v", same, different)
	}
}

func TestProjectRemovalAndReconcileCoverEveryVersion(t *testing.T) {
	service, values, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "sweep-1",
		Name: "Replaced twice", Purpose: "sweep coverage", Value: "entered-value-sweep-001",
	})
	testutil.FailErr(t, "create settings secret", err)
	for _, value := range []string{"entered-value-sweep-002", "entered-value-sweep-003"} {
		_, err = service.ReplaceValue(t.Context(), ReplaceValueRequest{
			ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: value,
		})
		testutil.FailErr(t, "replace value", err)
	}
	if ids := values.IDs(); len(ids) != 3 {
		t.Fatalf("stored versions = %v", ids)
	}
	// Every version is named by a row, so reconciliation keeps all three.
	testutil.FailErr(t, "reconcile", service.Reconcile(t.Context()))
	if ids := values.IDs(); len(ids) != 3 {
		t.Fatalf("reconcile removed retained versions: %v", ids)
	}

	remove, err := service.ProjectRemoval(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "snapshot project removal", err)
	testutil.FailErr(t, "remove project values", remove())
	if ids := values.IDs(); len(ids) != 0 {
		t.Fatalf("values after project removal = %v", ids)
	}
}

func TestProjectScreeningEvidenceExpiresWithoutAChatOrMutation(t *testing.T) {
	service, _, _ := testService(t)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "expiry-screen", Name: "Expiring token", Purpose: "Tests timed screening", Value: "expiry-fixture-token"})
	testutil.FailErr(t, "create expiring secret", err)
	_, err = service.Update(t.Context(), UpdateRequest{ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference,
		AgentUseDeadline: &AgentUseDeadline{At: now.Add(time.Minute).Format(time.RFC3339)}})
	testutil.FailErr(t, "set expiry", err)
	for _, expired := range []bool{false, true} {
		if expired {
			now = now.Add(time.Minute)
		}
		values, readErr := service.ScreeningValues(t.Context(), testdbseed.DefaultProjectID, "")
		testutil.FailErr(t, "read project evidence", readErr)
		if len(values) != 1 || (values[0].Reference == "") != expired || !values[0].NonDisclosable {
			t.Fatalf("expired=%v: evidence state is incorrect", expired)
		}
		// An ended agent-use window stops spending, not the capability.
		if values[0].Retired {
			t.Fatalf("expired=%v: a current capability read as retired", expired)
		}
	}
}

// Who reads a screen decides whether a reference attaches; it never decides
// whether the bytes are retired.
func TestScreeningAudiencesShareStandingNotReferences(t *testing.T) {
	service, _, _ := testService(t)
	reference := putChatSecret(t, service, "audience", "root-1", "audience-chat-value")
	audiences := []struct {
		label         string
		read          func() ([]secretmatch.Remembered, error)
		spendsLiveRef bool
	}{
		{"owning chat", func() ([]secretmatch.Remembered, error) {
			return service.ScreeningValues(t.Context(), testdbseed.DefaultProjectID, "root-1")
		}, true},
		{"another chat", func() ([]secretmatch.Remembered, error) {
			return service.ScreeningValues(t.Context(), testdbseed.DefaultProjectID, "root-2")
		}, false},
		{"project review", func() ([]secretmatch.Remembered, error) {
			return service.ReviewScreeningValues(t.Context(), testdbseed.DefaultProjectID)
		}, true},
	}
	for _, retired := range []bool{false, true} {
		if retired {
			_, err := service.RevokeByAgent(t.Context(), testdbseed.DefaultProjectID, "root-1", reference)
			testutil.FailErr(t, "revoke from the owning chat", err)
		}
		for _, audience := range audiences {
			values, err := audience.read()
			testutil.FailErr(t, "read "+audience.label+" evidence", err)
			if len(values) != 1 {
				t.Fatalf("%s: evidence = %d values", audience.label, len(values))
			}
			wantReference := ""
			if audience.spendsLiveRef && !retired {
				wantReference = reference
			}
			if values[0].Retired != retired || values[0].Reference != wantReference {
				t.Errorf("%s retired=%v: got retired=%v reference=%q",
					audience.label, retired, values[0].Retired, values[0].Reference)
			}
		}
	}
}

// Custody names who supplied the current bytes: a person's replacement makes
// even a host-generated value theirs.
func TestReplacementTakesThePersonsCustody(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "generated-then-replaced", Name: "Rotated", Purpose: "rotate", Scope: ScopeChat,
	})
	testutil.FailErr(t, "generate", err)
	if meta.Custody != CustodyChat {
		t.Fatalf("generated custody = %q", meta.Custody)
	}
	replaced, err := service.ReplaceValue(t.Context(), ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "person-supplied-rotation",
	})
	testutil.FailErr(t, "replace value", err)
	if replaced.Custody != CustodyPerson {
		t.Fatalf("replaced custody = %q", replaced.Custody)
	}
}

// A jar's values reach services through the jar, so a person cannot place
// their own bytes in one.
func TestReplaceValueRefusesAJar(t *testing.T) {
	service, _, _ := testService(t)
	site, err := url.Parse("http://localhost:5555/login")
	testutil.FailErr(t, "parse url", err)
	jar, err := service.OpenCookieJar(t.Context(), jarRequest("jar-replace"))
	testutil.FailErr(t, "open jar", err)
	jar.Store.SetCookies(site, []*http.Cookie{{Name: "session", Value: "cookie-value-replace", Path: "/"}})
	meta, err := service.SaveCookieJar(t.Context(), jarRequest("jar-replace"), jar)
	testutil.FailErr(t, "save jar", err)
	if meta.Custody != CustodyHost {
		t.Fatalf("jar custody = %q", meta.Custody)
	}
	if _, err := service.ReplaceValue(t.Context(), ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "person-supplied-jar",
	}); !errors.Is(err, ErrInvalidPut) {
		t.Fatalf("jar replacement error = %v", err)
	}
}

// An entry whose shape the vault does not recognize stays untouched and
// reports unavailable.
func TestUnknownVaultEntryReportsUnavailable(t *testing.T) {
	service, values, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "unknown-shape",
		Name: "Unknown", Purpose: "shape", Value: "settings-unknown-shape",
	})
	testutil.FailErr(t, "create settings secret", err)
	valueID := currentValueID(t, service, meta.Reference)
	testutil.FailErr(t, "write a bare value", values.Set(valueID, "settings-unknown-shape"))
	described, err := service.Describe(t.Context(), testdbseed.DefaultProjectID, "", meta.Reference)
	testutil.FailErr(t, "describe", err)
	if described.State != StateUnavailable || described.Custody != "" {
		t.Fatalf("unknown entry metadata = %+v", described)
	}
	if raw, ok := values.Get(valueID); !ok || raw.Value() != "settings-unknown-shape" {
		t.Fatal("an unknown entry was modified")
	}
}
