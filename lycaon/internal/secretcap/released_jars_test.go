package secretcap

import (
	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReleasedHTTPJarsReopenAfterArchiveRestore(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	store := testdbfixture.OpenPath(t, path)
	testdbseed.InsertProject(t, store, testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, store, "root-1", testdbseed.DefaultProjectID)
	slot := credentialstore.Slot{Path: filepath.Join(root, credentialstore.VaultBasename), Namespace: credentialstore.NamespaceManagedSecrets, Context: "released HTTP jars"}
	openVault := func() *credentialstore.Store {
		t.Helper()
		values, err := credentialstore.Open(slot, validID)
		testutil.FailErr(t, "open vault", err)
		return values
	}
	service := NewWithStore(store, openVault(), nil)
	ctx := t.Context()
	cookieRequest := jarRequest("open-cookie")
	tokenRequest := tokenJarRequest("open-token")
	seed := func(name, origin, purpose, document string) string {
		t.Helper()
		result, err := service.Put(ctx, PutRequest{
			ProjectID: cookieRequest.ProjectID, ChatSessionID: cookieRequest.ChatSessionID,
			SessionID: cookieRequest.SessionID, OperationID: "seed-" + name,
			Scope: ScopeChat, Name: name, Origin: origin, Purpose: purpose, Value: document,
		})
		testutil.FailErr(t, "store released jar", err)
		return result.Metadata.Reference
	}
	// Freeze the v1.0.2 vault documents independently of the current encoders.
	cookieRef := seed(cookieRequest.Name, OriginCookieJar, cookieJarPurpose,
		`{"cookies":[{"name":"sid","value":"fixture-cookie","domain":"service.example","host_only":true,"path":"/private","secure":true,"http_only":true,"created":"2026-01-01T00:00:00Z"}]}`)
	tokenRef := seed(tokenRequest.Name, OriginTokenJar, tokenJarPurpose,
		`{"session":{"value":"fixture-token","origin":"service.example@fixture","origin_label":"https://service.example:443"}}`)

	archive := filepath.Join(t.TempDir(), "backup.zip")
	_, err := backup.Create(ctx, backup.CreateOpts{ConfigDir: root, DBPath: path, SQLDB: store, AppVersion: "test", SchemaUserVersion: db.SchemaVersion}, archive)
	testutil.FailErr(t, "create archive", err)
	name := "changed-after-backup"
	_, err = service.Update(ctx, UpdateRequest{ProjectID: cookieRequest.ProjectID, Reference: cookieRef, Name: &name})
	testutil.FailErr(t, "rename jar after backup", err)
	_, err = backup.Stage(ctx, backup.StageOpts{ConfigDir: root, DBPath: path, SQLDB: store, ArchivePath: archive, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage archive", err)
	testutil.FailErr(t, "close store before restore", store.Close())
	testutil.FailErr(t, "apply archive", backup.ApplyPending(ctx, root))
	reopened := NewWithStore(testdbfixture.OpenPath(t, path), openVault(), nil)
	testutil.FailErr(t, "reconcile vault", reopened.Reconcile(ctx))
	assertDurableValues(t, reopened, "fixture-cookie", "fixture-token")
	cookies, err := reopened.Jars.OpenCookieJar(ctx, cookieRequest)
	testutil.FailErr(t, "open released cookies", err)
	target := &url.URL{Scheme: "https", Host: "service.example", Path: "/private"}
	selected := cookies.Store.Selected(target)
	if cookies.Reference != cookieRef || len(selected) != 1 || selected[0].Value != "fixture-cookie" ||
		!selected[0].HTTPOnly || selected[0].NativeIssuer != "" {
		t.Fatal("released cookie lost its reference, value, attributes or issuer")
	}
	target.Scheme = "http"
	if len(cookies.Store.Selected(target)) != 0 {
		t.Fatal("secure cookie selected for cleartext")
	}
	tokens, err := reopened.Jars.OpenTokenJar(ctx, tokenRequest)
	testutil.FailErr(t, "open released tokens", err)
	token, ok := tokens.Lookup("session")
	if !ok || tokens.Reference != tokenRef || token != (Token{Value: "fixture-token", Origin: "service.example@fixture", OriginLabel: "https://service.example:443"}) {
		t.Fatal("released token lost its reference, value or issuer")
	}
}
