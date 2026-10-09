package app

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func managedScreeningFixture(t *testing.T) (*serveBuilder, *secretmatch.Matcher, *sessionstore.SQL, *credentialstore.Store) {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "root-1", testdbseed.DefaultProjectID)
	values := credentialstore.NewEmpty(credentialstore.Slot{
		Path:      filepath.Join(t.TempDir(), credentialstore.VaultBasename),
		Namespace: credentialstore.NamespaceManagedSecrets, Context: "durable screening test",
	}, func(string) bool { return true })
	b := &serveBuilder{ctx: t.Context(), db: database}
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x5a}, 32))
	testutil.FailErr(t, "build fingerprinter", err)
	matcher.SetFingerprinter(fp)
	sessionWiring{b}.wireSecretEvidence(matcher, fp)
	b.secretCaps = secretcap.NewWithStore(database, values, func(root string, values []secretmatch.Remembered) { b.secretHarvest.Remember(root, values...) })
	sessionWiring{b}.wireMessageSecretRedaction(matcher)
	t.Cleanup(func() { sessionstore.SetMessageRedactor(nil); observability.SetCaptureRedactor(nil) })
	return b, matcher, sessionstore.NewSQL(database), values
}

func TestManagedSecretsAreScreenedBeforeTranscriptPersistenceAndPublication(t *testing.T) {
	b, matcher, store, values := managedScreeningFixture(t)
	database := store.DB()
	owner := testdbseed.OwnerID(t, database)
	raw := "orchard-secret-synthetic-only-9d41"
	put, err := b.secretCaps.Put(t.Context(), secretcap.PutRequest{
		ProjectID: testdbseed.DefaultProjectID, OperationID: "file-mark", Name: "Orchard file token",
		Purpose: "durable screening test", Scope: secretcap.ScopeProject, Origin: secretcap.OriginFileMarked,
		PersonID: owner, Value: raw,
	})
	testutil.FailErr(t, "mark file secret without a chat", err)

	for _, role := range []wire.MessageRole{wire.MessageRoleUser, wire.MessageRoleAssistant, wire.MessageRoleTool} {
		t.Run(string(role), func(t *testing.T) {
			msg := wire.Message{ID: string(role), Role: role, Content: "Public first\ntoken=" + raw + "\nPublic last"}
			batch := []wire.Message{msg}
			testutil.FailErr(t, "append raw message", store.AppendMessages(t.Context(), "root-1", batch...))
			assertManagedMessageScreened(t, batch[0], raw)
			var content string
			testutil.FailErr(t, "inspect actual durable bytes", database.QueryRowContext(t.Context(), "SELECT content FROM messages WHERE id = ?", msg.ID).Scan(&content))
			if strings.Contains(content, raw) {
				t.Fatal("raw managed value was persisted")
			}
			updated, err := store.UpdateMessage(t.Context(), "root-1", msg.ID, msg)
			testutil.FailErr(t, "replace raw message", err)
			assertManagedMessageScreened(t, updated, raw)
		})
	}
	// The general matcher also covers non-transcript capture owners.
	safe := observability.RedactCaptureText("diagnostic token=" + raw)
	if strings.Contains(safe, raw) {
		t.Fatal("unattributed diagnostic retained managed bytes")
	}
	// Scoped request evidence supplies the current capability reference.
	evidence, err := b.secretCaps.ScreeningValues(t.Context(), testdbseed.DefaultProjectID, "root-1")
	testutil.FailErr(t, "resolve request evidence", err)
	request := secretmatch.WithScreeningValues(t.Context(), evidence)
	projected, _ := llm.RedactMessageForStorage(request, matcher, wire.Message{Content: raw})
	if projected.Content != put.Metadata.Reference {
		t.Fatalf("current request lost its scoped handle: %q", projected.Content)
	}

	_, err = b.secretCaps.ReplaceValue(t.Context(), secretcap.ReplaceValueRequest{ProjectID: testdbseed.DefaultProjectID, Reference: put.Metadata.Reference, Value: "orchard-rotated-secret-only"})
	testutil.FailErr(t, "rotate project secret", err)
	_, err = b.secretCaps.RevokeProject(t.Context(), testdbseed.DefaultProjectID, put.Metadata.Reference, owner)
	testutil.FailErr(t, "revoke project secret", err)
	b.secretCaps = secretcap.NewWithStore(database, values, nil)
	testutil.FailErr(t, "restore evidence after restart", b.secretCaps.Reconcile(t.Context()))
	retained, _ := llm.RedactMessageForStorage(t.Context(), matcher, wire.Message{Content: raw + " orchard-rotated-secret-only"})
	assertManagedMessageScreened(t, retained, raw, "orchard-rotated-secret-only")
}

func assertManagedMessageScreened(t *testing.T, message wire.Message, secrets ...string) {
	t.Helper()
	encoded, err := json.Marshal(message)
	testutil.FailErr(t, "marshal publication", err)
	for _, secret := range secrets {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("publication retained managed plaintext")
		}
	}
	if message.HostSecretRedaction == nil || message.HostSecretRedaction.Occurrences() == 0 {
		t.Fatal("screened publication lacks host provenance")
	}
}

func TestMarkingAProjectSecretRescreensExistingTaskHistory(t *testing.T) {
	for name, cancelOnCommit := range map[string]bool{"active request": false, "canceled after commit": true} {
		t.Run(name, func(t *testing.T) {
			b, matcher, store, _ := managedScreeningFixture(t)
			raw := "orchard-retrospective-secret-91d4"
			testutil.FailErr(t, "append before marking", store.AppendMessages(t.Context(), "root-1", wire.Message{ID: "old-tool", Role: wire.MessageRoleTool, Content: "token=" + raw}))
			requestCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelOnCommit {
				b.secretCaps.AddScreeningInvalidationObserver(func(context.Context, string) { cancel() })
			}
			b.mgr = session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
			sessionWiring{b}.wireMessageSecretRedaction(matcher)
			_, err := b.secretCaps.Put(requestCtx, secretcap.PutRequest{
				ProjectID: testdbseed.DefaultProjectID, OperationID: "late-mark", Name: "Late token", Purpose: "screen earlier reads",
				Scope: secretcap.ScopeProject, Origin: secretcap.OriginFileMarked, PersonID: testdbseed.OwnerID(t, store.DB()), Value: raw,
			})
			testutil.FailErr(t, "mark an already-read value", err)
			if cancelOnCommit && requestCtx.Err() == nil {
				t.Fatal("fixture did not cancel the committed request")
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				messages, err := store.GetMessages(t.Context(), "root-1")
				testutil.FailErr(t, "read rescreened history", err)
				if len(messages) == 1 && !strings.Contains(messages[0].Content, raw) {
					assertManagedMessageScreened(t, messages[0], raw)
					return
				}
				if time.Now().After(deadline) {
					t.Fatal("marking a project secret left earlier tool plaintext in storage")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
