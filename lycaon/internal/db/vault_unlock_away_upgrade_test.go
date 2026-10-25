package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVaultUnlockAwayMigrationKeepsAuditsAndAdmitsAway(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "upgrade-corpus", "1.0.3-rc1", "store.db"))
	testutil.FailErr(t, "read released corpus", err)
	path := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "copy released corpus", os.WriteFile(path, raw, 0600))
	source, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open released store", err)
	released, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect released store", err)
	if released != migrations.SourceManifestLayout(released).From {
		t.Fatalf("released store is %+v, not the registered revision-4 source", released)
	}
	var project, person string
	testutil.FailErr(t, "read released owner", source.QueryRow(`SELECT (SELECT id FROM projects LIMIT 1), (SELECT id FROM people LIMIT 1)`).Scan(&project, &person))
	_, err = source.Exec(`INSERT INTO vault_unlocks(id,project_id,chat_session_id,person_id,authenticator,window_label,unlocked_at,ended_at,end_reason) VALUES('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',?,'root-1',?,'linux_user_presence','main',?,?,'screen_locked')`, project, person, presenceAuditTimestamp, presenceAuditTimestamp)
	testutil.FailErr(t, "seed released unlock", err)
	_, err = source.Exec(`INSERT INTO vault_unlocks(id,project_id,chat_session_id,person_id,authenticator,window_label,unlocked_at,ended_at,end_reason) VALUES('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',?,'root-1',?,'linux_user_presence','main',?,?,'away')`, project, person, presenceAuditTimestamp, presenceAuditTimestamp)
	if err == nil {
		t.Fatal("the released audit already admits away")
	}
	before := namespaceTableContents(t, source, "vault_unlocks")
	testutil.FailErr(t, "close released store", source.Close())

	testutil.FailErr(t, "upgrade released store", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer func() { _ = upgraded.Close() }()
	if got := namespaceTableContents(t, upgraded, "vault_unlocks"); got != before {
		t.Fatal("migration changed retained unlock audits")
	}
	want, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "read current baseline", err)
	got, err := inspectSchema(t.Context(), upgraded)
	testutil.FailErr(t, "inspect upgraded store", err)
	if got != want {
		t.Fatalf("upgraded baseline %+v, want %+v", got, want)
	}
	_, err = upgraded.Exec(`INSERT INTO vault_unlocks(id,project_id,chat_session_id,person_id,authenticator,window_label,unlocked_at,ended_at,end_reason) VALUES('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',?,'root-1',?,'linux_user_presence','main',?,?,'away')`, project, person, presenceAuditTimestamp, presenceAuditTimestamp)
	testutil.FailErr(t, "record an away unlock end", err)
	_, err = upgraded.Exec(`INSERT INTO vault_unlocks(id,project_id,chat_session_id,person_id,authenticator,window_label,unlocked_at,ended_at,end_reason) VALUES('cccccccc-cccc-4ccc-8ccc-cccccccccccc',?,'root-1',?,'linux_user_presence','main',?,?,'elsewhere')`, project, person, presenceAuditTimestamp, presenceAuditTimestamp)
	if err == nil {
		t.Fatal("upgraded unlock audit accepted an unknown end reason")
	}
}
