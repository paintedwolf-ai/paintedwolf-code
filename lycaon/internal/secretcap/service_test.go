package secretcap

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGenerateResolveAndListNeverReturnTheValue(t *testing.T) {
	service, values, remembered := testService(t)
	meta, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "call-1", Name: "registry token", Purpose: "authenticate a local registry",
	})
	testutil.FailErr(t, "generate secret", err)
	value, ok := storedEntry(values, currentValueID(t, service, meta.Reference))
	if !ok || len(value.Value) < 32 {
		t.Fatal("generated value was not stored")
	}
	if strings.Contains(meta.Reference, value.Value) || meta.State != "active" || meta.EntropyBits != 256 {
		t.Fatalf("unsafe or incomplete metadata: %+v", meta)
	}
	if len(*remembered) != 1 || (*remembered)[0].Secret != value.Value {
		t.Fatal("generated value was not admitted to exact-match screening")
	}

	canonical := map[string]any{"headers": []any{map[string]any{"value": "Bearer " + meta.Reference}}}
	resolved, err := service.Resolve(t.Context(), canonical, ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1",
	})
	testutil.FailErr(t, "resolve reference", err)
	resolvedHeader := resolved.Arguments["headers"].([]any)[0].(map[string]any)["value"].(string)
	if resolvedHeader != "Bearer "+value.Value {
		t.Fatal("execution copy did not receive the secret")
	}
	canonicalHeader := canonical["headers"].([]any)[0].(map[string]any)["value"].(string)
	if canonicalHeader != "Bearer "+meta.Reference {
		t.Fatal("resolver mutated canonical arguments")
	}
	listed, err := service.List(t.Context(), testdbseed.DefaultProjectID, "root-1")
	testutil.FailErr(t, "list references", err)
	if len(listed) != 1 || listed[0].Reference != meta.Reference {
		t.Fatalf("listed metadata = %+v", listed)
	}
	encoded := listed[0].Reference + listed[0].Name + listed[0].Purpose
	if strings.Contains(encoded, value.Value) {
		t.Fatal("list exposed the secret value")
	}
}

func TestProjectScopeRequiresPurpose(t *testing.T) {
	service, _, _ := testService(t)
	_, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "project-without-purpose", Name: "project token", Scope: ScopeProject,
	})
	if !errors.Is(err, ErrInvalidGenerate) {
		t.Fatalf("generate error = %v want ErrInvalidGenerate", err)
	}
	_, err = service.Put(t.Context(), PutRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "put-project-without-purpose", Name: "project token", Scope: ScopeProject,
		Origin: OriginAskUserResponse, PersonID: testOwner(t, service), Value: "project-secret-value",
	})
	if !errors.Is(err, ErrInvalidPut) {
		t.Fatalf("put error = %v want ErrInvalidPut", err)
	}
}

func TestPutTracksUserAndDetectedValuesWithoutGeneratedClaims(t *testing.T) {
	service, values, remembered := testService(t)
	for _, origin := range []string{OriginAskUserResponse, OriginDetected} {
		put, err := service.Put(t.Context(), PutRequest{
			ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
			OperationID: "put-" + origin, Name: origin + " token", Scope: ScopeChat,
			Origin: origin, PersonID: putPerson(t, service, origin), Value: origin + "-secret-value",
		})
		testutil.FailErr(t, "put "+origin, err)
		meta := put.Metadata
		if meta.Origin != origin || meta.Format != "" || meta.EntropyBits != 0 {
			t.Fatalf("metadata = %+v", meta)
		}
		if value, ok := storedEntry(values, currentValueID(t, service, meta.Reference)); !ok || value.Value != origin+"-secret-value" {
			t.Fatalf("protected value = %q ok=%v", value.Value, ok)
		}
	}
	if len(*remembered) != 2 || (*remembered)[0].Reference == "" || !(*remembered)[0].NonDisclosable {
		t.Fatalf("remembered evidence = %+v", *remembered)
	}
}

func TestDetectedMetadataFitsStorageWithoutChangingProtectedValue(t *testing.T) {
	for _, name := range []string{"", "stripe token", strings.Repeat("x", 80), strings.Repeat("x", 81), strings.Repeat("🐺", 120)} {
		t.Run(fmt.Sprintf("name-runes-%d", utf8.RuneCountInString(name)), func(t *testing.T) {
			service, values, remembered := testService(t)
			const value = "synthetic-detected-credential-7912"
			request := PutRequest{
				ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
				OperationID: "detected-fingerprint", Name: name, Purpose: strings.Repeat("é", 300),
				Scope: ScopeChat, Origin: OriginDetected, Value: value,
			}
			put, err := service.Put(t.Context(), request)
			testutil.FailErr(t, "protect detected credential", err)
			meta := put.Metadata
			if !utf8.ValidString(meta.Name) || utf8.RuneCountInString(meta.Name) < 1 || utf8.RuneCountInString(meta.Name) > 80 || utf8.RuneCountInString(meta.Purpose) > 240 {
				t.Fatalf("detector metadata exceeds storage contract: %+v", meta)
			}
			if got, ok := storedEntry(values, currentValueID(t, service, meta.Reference)); !ok || got.Value != value {
				t.Fatal("detector metadata normalization changed protected bytes")
			}
			if len(*remembered) != 1 || !(*remembered)[0].NonDisclosable || (*remembered)[0].Reference != meta.Reference {
				t.Fatal("detected credential was not admitted to reference screening")
			}
			repeated, err := service.Put(t.Context(), request)
			testutil.FailErr(t, "repeat detected adoption", err)
			if repeated.Created || repeated.Metadata.Reference != meta.Reference {
				t.Fatal("detected metadata changed operation idempotency")
			}
		})
	}
}

func TestHumanProvidedMetadataStillRequiresValidBounds(t *testing.T) {
	for _, origin := range []string{OriginAskUserResponse, OriginComposerMarked} {
		service, _, _ := testService(t)
		_, err := service.Put(t.Context(), PutRequest{
			ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
			OperationID: origin, Name: strings.Repeat("🐺", 81), Scope: ScopeChat,
			Origin: origin, PersonID: testOwner(t, service), Value: "synthetic-credential-value",
		})
		if !errors.Is(err, ErrInvalidPut) {
			t.Fatalf("%s metadata bounds were silently changed: %v", origin, err)
		}
	}
}

// Values at or above the screen floor are stored byte-for-byte, edges included.
func TestPutStoresFloorLengthAndWhitespaceBearingValuesExactly(t *testing.T) {
	service, values, _ := testService(t)
	for i, want := range []string{"abcdef", " \tcredential with edges\n "} {
		put, err := service.Put(t.Context(), PutRequest{
			ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
			OperationID: fmt.Sprintf("exact-%d", i), Name: fmt.Sprintf("exact %d", i), Scope: ScopeChat,
			Origin: OriginAskUserResponse, PersonID: testOwner(t, service), Value: want,
		})
		testutil.FailErr(t, "put exact value", err)
		meta := put.Metadata
		if got, ok := storedEntry(values, currentValueID(t, service, meta.Reference)); !ok || got.Value != want {
			t.Fatalf("stored value = %q, %v; want exact %q", got.Value, ok, want)
		}
	}
	_, err := service.Put(t.Context(), PutRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "empty", Name: "empty", Scope: ScopeChat, Origin: OriginAskUserResponse,
		PersonID: testOwner(t, service),
	})
	if !errors.Is(err, ErrInvalidPut) {
		t.Fatalf("empty value error = %v want ErrInvalidPut", err)
	}
}

func TestDiscardCreatedRemovesMetadataAndProtectedVersions(t *testing.T) {
	service, values, _ := testService(t)
	req := PutRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "discard-created", Name: "ephemeral", Scope: ScopeChat,
		Origin: OriginAskUserResponse, PersonID: testOwner(t, service), Value: "ephemeral-value-1",
	}
	put, err := service.Put(t.Context(), req)
	testutil.FailErr(t, "put", err)
	if !put.Created {
		t.Fatal("new capability was not marked as created")
	}
	replayed, err := service.Put(t.Context(), req)
	testutil.FailErr(t, "replay put", err)
	if replayed.Created || replayed.Metadata.Reference != put.Metadata.Reference {
		t.Fatalf("replayed result = %+v", replayed)
	}
	meta := put.Metadata
	valueID := currentValueID(t, service, meta.Reference)
	testutil.FailErr(t, "discard created", service.DiscardCreated(
		t.Context(), testdbseed.DefaultProjectID, meta.Reference,
	))
	if _, ok := values.Get(valueID); ok {
		t.Fatal("discarded protected version remains")
	}
	if listed, listErr := service.ListProject(t.Context(), testdbseed.DefaultProjectID); listErr != nil || len(listed) != 0 {
		t.Fatalf("list after discard = %+v, %v", listed, listErr)
	}
}

func TestResolveSubstitutesEveryNestedStringValueWithoutMutatingCanonicalArgs(t *testing.T) {
	service, values, _ := testService(t)
	meta, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "deep-resolution", Name: "nested token",
	})
	testutil.FailErr(t, "generate nested secret", err)
	value, ok := storedEntry(values, currentValueID(t, service, meta.Reference))
	if !ok {
		t.Fatal("generated nested value is unavailable")
	}
	canonical := map[string]any{
		"scalar": "prefix-" + meta.Reference + "-suffix",
		"array": []any{
			meta.Reference,
			map[string]any{"arbitrary_future_field": "Bearer " + meta.Reference},
		},
		"strings": []string{"one", meta.Reference},
	}
	resolved, err := service.Resolve(t.Context(), canonical, ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1",
	})
	testutil.FailErr(t, "resolve nested strings", err)
	if got := resolved.Arguments["scalar"]; got != "prefix-"+value.Value+"-suffix" {
		t.Fatalf("resolved scalar = %q", got)
	}
	array := resolved.Arguments["array"].([]any)
	if array[0] != value.Value || array[1].(map[string]any)["arbitrary_future_field"] != "Bearer "+value.Value {
		t.Fatalf("resolved array = %#v", array)
	}
	if got := resolved.Arguments["strings"].([]string); got[1] != value.Value {
		t.Fatalf("resolved string slice = %#v", got)
	}
	if got := canonical["scalar"]; got != "prefix-"+meta.Reference+"-suffix" {
		t.Fatalf("canonical scalar was mutated: %q", got)
	}
	canonicalArray := canonical["array"].([]any)
	if canonicalArray[1].(map[string]any)["arbitrary_future_field"] != "Bearer "+meta.Reference {
		t.Fatal("canonical nested map was mutated")
	}
}

func TestResolvePreservesReferenceSyntaxInObjectKeys(t *testing.T) {
	const key = "prefix-{{paintedwolf-secret:00000000-0000-4000-8000-000000000001}}"
	args := map[string]any{key: "literal value"}
	if ReferenceUseInSlots(args, nil) != (ReferenceUse{}) {
		t.Fatal("an object key triggered reference handling")
	}
	resolved, err := (&Service{}).Resolve(t.Context(), args, ResolveContext{})
	testutil.FailErr(t, "resolve literal object key", err)
	if resolved.Arguments[key] != "literal value" {
		t.Fatalf("object key changed: %#v", resolved)
	}
}

func TestScopesAndRevocationAreEnforcedAtResolution(t *testing.T) {
	service, values, _ := testService(t)
	chat, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "call-chat", Name: "chat token", Scope: ScopeChat,
	})
	testutil.FailErr(t, "generate chat secret", err)
	project, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "call-project", Name: "project token", Purpose: "authenticate future project chats", Scope: ScopeProject,
	})
	testutil.FailErr(t, "generate project secret", err)

	_, err = service.Resolve(t.Context(), map[string]any{"value": chat.Reference}, ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-2",
	})
	if !errors.Is(err, ErrNotVisible) {
		t.Fatalf("cross-chat resolution error = %v", err)
	}
	_, err = service.Resolve(t.Context(), map[string]any{"value": project.Reference}, ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-2",
	})
	testutil.FailErr(t, "resolve project secret from another chat", err)

	revoked, err := service.RevokeByAgent(t.Context(), testdbseed.DefaultProjectID, "root-1", chat.Reference)
	testutil.FailErr(t, "revoke chat secret", err)
	if revoked.State != "revoked" {
		t.Fatalf("revoked state = %q", revoked.State)
	}
	if _, ok := values.Get(currentValueID(t, service, chat.Reference)); !ok {
		t.Fatal("revocation removed provider-screening evidence")
	}
	_, err = service.Resolve(t.Context(), map[string]any{"value": chat.Reference}, ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1",
	})
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked resolution error = %v", err)
	}
}

func TestGenerationIsIdempotentByToolCall(t *testing.T) {
	service, values, _ := testService(t)
	req := GenerateRequest{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1", OperationID: "same-call", Name: "token"}
	first, err := service.Generate(t.Context(), req)
	testutil.FailErr(t, "first generate", err)
	second, err := service.Generate(t.Context(), req)
	testutil.FailErr(t, "replayed generate", err)
	if first.Reference != second.Reference || len(values.IDs()) != 1 {
		t.Fatalf("replay minted a second capability: first=%q second=%q ids=%v", first.Reference, second.Reference, values.IDs())
	}
	differentSession := req
	differentSession.ChatSessionID = "root-2"
	differentSession.SessionID = "root-2"
	third, err := service.Generate(t.Context(), differentSession)
	testutil.FailErr(t, "same call id in another session", err)
	if third.Reference == first.Reference || len(values.IDs()) != 2 {
		t.Fatalf("operation id leaked across sessions: first=%q third=%q ids=%v", first.Reference, third.Reference, values.IDs())
	}
}

func TestRememberProjectValuesIncludesRetiredChatSecrets(t *testing.T) {
	service, values, _ := testService(t)
	meta, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "chat-prime", Name: "retired deployment token", Scope: ScopeChat,
	})
	testutil.FailErr(t, "generate chat secret", err)
	want, ok := storedEntry(values, currentValueID(t, service, meta.Reference))
	if !ok {
		t.Fatal("generated value is unavailable")
	}
	_, err = service.RevokeByAgent(t.Context(), testdbseed.DefaultProjectID, "root-1", meta.Reference)
	testutil.FailErr(t, "revoke chat secret", err)

	var gotRoot string
	var got []secretmatch.Remembered
	service.remember = func(root string, values []secretmatch.Remembered) {
		gotRoot = root
		got = append(got, values...)
	}
	testutil.FailErr(t, "prime later chat", service.RememberProjectValues(
		t.Context(), testdbseed.DefaultProjectID, "root-2",
	))
	if gotRoot != "root-2" || len(got) != 1 || got[0].Secret != want.Value || !got[0].NonDisclosable {
		t.Fatalf("remembered root=%q values=%+v", gotRoot, got)
	}
}

func TestReconcileRetainsExpiredEvidenceAndPurgesOrphans(t *testing.T) {
	service, values, _ := testService(t)
	meta, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "agent-use-ending-call", Name: "short-lived token", AgentUseTTL: time.Minute,
	})
	testutil.FailErr(t, "generate expiring secret", err)
	retained := currentValueID(t, service, meta.Reference)
	orphanID := "00000000-0000-4000-8000-000000000001"
	testutil.FailErr(t, "store orphan", values.Set(orphanID, "orphan-secret-value-123456"))
	service.now = func() time.Time { return time.Date(2026, 8, 31, 12, 2, 0, 0, time.UTC) }
	testutil.FailErr(t, "reconcile", service.Reconcile(t.Context()))
	if _, ok := values.Get(retained); !ok {
		t.Fatal("expired provider-screening evidence was removed")
	}
	if _, ok := values.Get(orphanID); ok {
		t.Fatal("orphaned value survived reconciliation")
	}
	listed, err := service.List(t.Context(), testdbseed.DefaultProjectID, "root-1")
	testutil.FailErr(t, "list expired metadata", err)
	if len(listed) != 1 || listed[0].State != "agent_use_expired" {
		t.Fatalf("expired metadata = %+v", listed)
	}
}

func TestAlphanumericEntropyClaimIsConservative(t *testing.T) {
	value, bits, err := generateValue(FormatAlphanumeric, 16)
	testutil.FailErr(t, "generate alphanumeric", err)
	if bits != 128 || len(value)*5 < int(bits) {
		t.Fatalf("value length=%d entropy_bits=%d", len(value), bits)
	}
}

func TestProjectAdministrationIncludesChatMetadataAndRemovesEveryValue(t *testing.T) {
	service, values, _ := testService(t)
	for _, req := range []GenerateRequest{
		{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1", OperationID: "chat-admin", Name: "chat key", Scope: ScopeChat},
		{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1", OperationID: "project-admin", Name: "project key", Purpose: "authenticate future project chats", Scope: ScopeProject},
	} {
		_, err := service.Generate(t.Context(), req)
		testutil.FailErr(t, "generate administration fixture", err)
	}
	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list project secrets", err)
	if len(listed) != 2 || listed[0].Reference == listed[1].Reference {
		t.Fatalf("project inventory = %+v", listed)
	}

	revoked, err := service.RevokeProject(
		t.Context(),
		testdbseed.DefaultProjectID,
		listed[0].Reference,
		testOwner(t, service),
	)
	testutil.FailErr(t, "revoke through project administration", err)
	if revoked.State != "revoked" {
		t.Fatalf("revoke state = %q", revoked.State)
	}
	if _, ok := values.Get(currentValueID(t, service, revoked.Reference)); !ok {
		t.Fatal("project revocation removed provider-screening evidence")
	}

	remove, err := service.ProjectRemoval(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "snapshot project removal", err)
	testutil.FailErr(t, "remove project values", remove())
	if ids := values.IDs(); len(ids) != 0 {
		t.Fatalf("protected values after project removal = %v", ids)
	}
}

func testService(t *testing.T) (*Service, *credentialstore.Store, *[]secretmatch.Remembered) {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "root-1", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "root-2", testdbseed.DefaultProjectID)
	values := credentialstore.NewEmpty(credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets,
		Context:   "test managed secret",
	}, validID)
	remembered := &[]secretmatch.Remembered{}
	service := NewWithStore(database, values, func(_ string, items []secretmatch.Remembered) {
		*remembered = append(*remembered, items...)
	})
	service.now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	return service, values, remembered
}

// testOwner returns the person every test store seeds.
func testOwner(t *testing.T, service *Service) string {
	t.Helper()
	return testdbseed.OwnerID(t, service.handle)
}

// putPerson names the owner for person-supplied origins and nobody for agent ones.
func putPerson(t *testing.T, service *Service, origin string) string {
	t.Helper()
	if agentAuthoredOrigin(origin) {
		return ""
	}
	return testOwner(t, service)
}

// currentValueID returns the value id selected by a reference.
func currentValueID(t *testing.T, service *Service, reference string) string {
	t.Helper()
	id, err := ParseReference(reference)
	testutil.FailErr(t, "parse reference", err)
	current, ok, err := service.currentVersion(t.Context(), id)
	testutil.FailErr(t, "read current version", err)
	if !ok {
		t.Fatalf("no current version behind %s", reference)
	}
	return current.ID
}

func TestReferenceLimitCountsDistinctCapabilitiesAndWithholdsPartialResolution(t *testing.T) {
	service, _, _ := testService(t)
	references := make([]string, 0, maxReferencesPerCall+1)
	for i := 0; i <= maxReferencesPerCall; i++ {
		meta, err := service.Generate(t.Context(), GenerateRequest{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-limit", SessionID: "root-limit", OperationID: fmt.Sprintf("limit-%d", i), Name: fmt.Sprintf("token %d", i), Purpose: "bounded resolution"})
		testutil.FailErr(t, "generate bounded reference", err)
		references = append(references, meta.Reference)
	}
	access := ResolveContext{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-limit"}
	repeated := append([]string(nil), references[:maxReferencesPerCall]...)
	repeated = append(repeated, references[0], references[0])
	resolution, err := service.Resolve(t.Context(), map[string]any{"values": repeated}, access)
	testutil.FailErr(t, "resolve repeated references at distinct bound", err)
	resolution.Finish(t.Context())
	args := map[string]any{"values": references}
	resolution, err = service.Resolve(t.Context(), args, access)
	var limit *ReferenceLimitError
	if resolution != nil || !errors.As(err, &limit) || limit.Count != maxReferencesPerCall+1 || limit.Limit != maxReferencesPerCall {
		t.Fatalf("invalid bounded-resolution outcome: resolution=%v error=%v", resolution != nil, err)
	}
	if args["values"].([]string)[0] != references[0] {
		t.Fatal("failed resolution modified canonical arguments")
	}
}

// storedEntry reads one vault entry through its envelope.
func storedEntry(values *credentialstore.Store, versionID string) (protectedValue, bool) {
	return vaultValues{store: values}.get(versionID)
}
