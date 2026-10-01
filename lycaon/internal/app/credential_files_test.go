package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func credentialFilesFixture(t *testing.T, managed ...string) *credentialFiles {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "credential-files.db")
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO projects(id, name, last_opened_at, created_at) VALUES('proj-1','P','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed project", err)
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x3c}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	return &credentialFiles{
		harvest: secretharvest.NewRuntime(fp), fp: fp, queries: db.New(sqlDB),
		managed: func(string) []secretmatch.Remembered {
			out := make([]secretmatch.Remembered, 0, len(managed))
			for _, value := range managed {
				out = append(out, secretmatch.Remembered{Secret: value, NonDisclosable: true})
			}
			return out
		},
		exposure: func(context.Context, string, string) error { return nil },
	}
}

func harvestedSecrets(files *credentialFiles, root string) map[string]bool {
	out := map[string]bool{}
	for _, v := range files.harvest.ValuesFor(root) {
		out[v.Secret()] = true
	}
	return out
}

// Reading back a file the model wrote harvests only what the model did not
// author and no managed secret protects: the loop the agent's own write caused.
func TestCredentialReadSkipsAuthoredAndManagedValues(t *testing.T) {
	const materialized = "a0VBKPXTbWzsjo2Il49ko0ng"
	files := credentialFilesFixture(t, materialized)
	ctx := context.Background()
	testutil.FailErr(t, "record authorship", files.Authored(ctx, tools.AuthoredCredentialValues{
		ProjectID: "proj-1", RootSessionID: "root", SessionID: "root", ToolCallID: "call-1",
		RootID: "r1", Path: ".env", Values: []string{"todo-web-client"},
	}))
	testutil.FailErr(t, "deliver read", files.Delivered(ctx, tools.CredentialFileRead{
		ProjectID: "proj-1", RootSessionID: "root", RootID: "r1", Path: ".env", Container: ".env",
		Content: "OIDC_CLIENT_ID=todo-web-client\nSESSION_SECRET=" + materialized + "\nAPI_TOKEN=person-typed-token\n",
	}))
	got := harvestedSecrets(files, "root")
	if got["todo-web-client"] {
		t.Fatal("a value the model wrote was harvested as a new secret")
	}
	if got[materialized] {
		t.Fatal("a managed value was harvested as a second secret")
	}
	if !got["person-typed-token"] || len(got) != 1 {
		t.Fatalf("harvested %v, want only the value nobody else accounts for", got)
	}
}

// Authorship binds to one location: the same bytes in another credential file
// are still that file's evidence.
func TestCredentialAuthorshipIsPerLocation(t *testing.T) {
	files := credentialFilesFixture(t)
	ctx := context.Background()
	testutil.FailErr(t, "record authorship", files.Authored(ctx, tools.AuthoredCredentialValues{
		ProjectID: "proj-1", RootSessionID: "root", SessionID: "root", ToolCallID: "call-1",
		RootID: "r1", Path: ".env", Values: []string{"shared-value-1234"},
	}))
	testutil.FailErr(t, "deliver read", files.Delivered(ctx, tools.CredentialFileRead{
		ProjectID: "proj-1", RootSessionID: "root", RootID: "r1", Path: "secrets/.env", Container: "secrets/.env",
		Content: "TOKEN=shared-value-1234\n",
	}))
	if !harvestedSecrets(files, "root")["shared-value-1234"] {
		t.Fatal("authorship in one file exempted another file's evidence")
	}
}

// A value the session tree already holds as evidence came from a protected
// source, not from the model, so writing it does not record authorship.
func TestCredentialAuthorshipSkipsKnownEvidence(t *testing.T) {
	files := credentialFilesFixture(t)
	ctx := context.Background()
	testutil.FailErr(t, "deliver read", files.Delivered(ctx, tools.CredentialFileRead{
		ProjectID: "proj-1", RootSessionID: "root", RootID: "r1", Path: "person/.env", Container: "person/.env",
		Content: "TOKEN=person-typed-token\n",
	}))
	testutil.FailErr(t, "record authorship", files.Authored(ctx, tools.AuthoredCredentialValues{
		ProjectID: "proj-1", RootSessionID: "root", SessionID: "root", ToolCallID: "call-1",
		RootID: "r1", Path: ".env", Values: []string{"person-typed-token"},
	}))
	authored, err := files.queries.ListCredentialAuthoredValues(ctx, db.ListCredentialAuthoredValuesParams{
		ProjectID: "proj-1", RootID: "r1", Path: ".env",
	})
	testutil.FailErr(t, "list authorship", err)
	if len(authored) != 0 {
		t.Fatalf("a copied secret was recorded as model-authored: %v", authored)
	}
}

// Exposure is recorded for the reading chat before any evidence is admitted.
func TestCredentialReadRecordsExposureFirst(t *testing.T) {
	files := credentialFilesFixture(t)
	var session, path string
	files.exposure = func(_ context.Context, sessionID, container string) error {
		session, path = sessionID, container
		return errors.New("evidence store unavailable")
	}
	err := files.Delivered(context.Background(), tools.CredentialFileRead{
		ProjectID: "proj-1", SessionID: "chat-1", RootSessionID: "root", Container: "web/.env",
		Content: "API_TOKEN=person-typed-token\n",
	})
	if err == nil {
		t.Fatal("a failed exposure record admitted the read")
	}
	if session != "chat-1" || path != "web/.env" {
		t.Fatalf("exposure recorded for %q at %q", session, path)
	}
	if len(harvestedSecrets(files, "root")) != 0 {
		t.Fatal("evidence was admitted without an exposure record")
	}
}
