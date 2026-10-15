package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

const presenceAuditTimestamp = "2026-10-02T09:00:00.000000000Z"

func TestPresenceAuthenticatorMigrationKeepsAuditsAndAdmitsLinux(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "upgrade-corpus", "1.0.2-rc2", "store.db"))
	testutil.FailErr(t, "read released corpus", err)
	path := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "copy released corpus", os.WriteFile(path, raw, 0600))
	source, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open released store", err)
	released, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect released store", err)
	if released != migrations.PresenceAuthenticators(released).From {
		t.Fatalf("released store is %+v, not the registered revision-2 source", released)
	}
	var project, person string
	testutil.FailErr(t, "read released owner", source.QueryRow(`SELECT (SELECT id FROM projects LIMIT 1), (SELECT id FROM people LIMIT 1)`).Scan(&project, &person))
	_, err = source.Exec(`INSERT INTO managed_secrets(id,project_id,created_by_person_id,scope,name,origin,operation_id,created_at) VALUES('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',?,?,'project','Released key','settings_entered','released-op',?)`, project, person, presenceAuditTimestamp)
	testutil.FailErr(t, "seed released secret", err)
	insertPresenceAudits(t, source, project, person, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "macos_user_presence")
	before := map[string]string{}
	for _, table := range []string{"managed_secret_reveals", "vault_unlocks"} {
		before[table] = namespaceTableContents(t, source, table)
	}
	testutil.FailErr(t, "close released store", source.Close())

	testutil.FailErr(t, "upgrade released store", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer func() { _ = upgraded.Close() }()
	for table, rows := range before {
		if got := namespaceTableContents(t, upgraded, table); got != rows {
			t.Fatalf("migration changed retained %s", table)
		}
	}
	want, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "read current baseline", err)
	got, err := inspectSchema(t.Context(), upgraded)
	testutil.FailErr(t, "inspect upgraded store", err)
	if got != want {
		t.Fatalf("upgraded baseline %+v, want %+v", got, want)
	}
	insertPresenceAudits(t, upgraded, project, person, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "linux_user_presence")
	insertPresenceAudits(t, upgraded, project, person, "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "linux_app_password")
	_, err = upgraded.Exec(`INSERT INTO vault_unlocks(id,project_id,chat_session_id,person_id,authenticator,window_label,unlocked_at) VALUES('eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee',?,'root-1',?,'unknown_presence','main',?)`, project, person, presenceAuditTimestamp)
	if err == nil {
		t.Fatal("upgraded unlock audit accepted an unknown authenticator")
	}
	_, err = upgraded.Exec(`INSERT INTO managed_secret_reveals(id,secret_id,version,authenticator,window_label,person_id,revealed_at) VALUES('eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee','aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',1,'unknown_presence','main',?,?)`, person, presenceAuditTimestamp)
	if err == nil {
		t.Fatal("upgraded reveal audit accepted an unknown authenticator")
	}
}

func insertPresenceAudits(t *testing.T, database *sql.DB, project, person, id, authenticator string) {
	t.Helper()
	_, err := database.Exec(`INSERT INTO managed_secret_reveals(id,secret_id,version,authenticator,window_label,person_id,revealed_at) VALUES(?,'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',1,?,'main',?,?)`, id, authenticator, person, presenceAuditTimestamp)
	testutil.FailErr(t, "record "+authenticator+" reveal", err)
	_, err = database.Exec(`INSERT INTO vault_unlocks(id,project_id,chat_session_id,person_id,authenticator,window_label,unlocked_at,ended_at,end_reason) VALUES(?,?,'root-1',?,?,'main',?,?,'idle')`, id, project, person, authenticator, presenceAuditTimestamp, presenceAuditTimestamp)
	testutil.FailErr(t, "record "+authenticator+" unlock", err)
}
